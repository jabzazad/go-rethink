// Package store persists webhook events, handled messages (with every decider's
// verdict), AI calls and eval runs to MongoDB, so comparisons survive restarts and
// can be sliced by time range.
//
// Collections:
//
//	webhook_events  raw LINE webhook bodies, with signature result
//	messages        one doc per handled message: decision outcome, all verdicts, reply, latency, cost
//	ai_logs         one doc per Gemini call: prompt, reply, tokens, cost, latency
//	evals           eval-set runs
//
// Documents use the same field names as the JSON API (converted via JSON), plus a
// BSON date "ts" for range queries.
package store

import (
	"context"
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"poc-gateway/internal/decision"
	"poc-gateway/internal/stats"
)

type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

func New(ctx context.Context, uri, dbName string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pctx, nil); err != nil {
		return nil, err
	}
	s := &Store{client: client, db: client.Database(dbName)}
	for _, c := range []string{"webhook_events", "messages", "ai_logs", "evals"} {
		_, err := s.db.Collection(c).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "ts", Value: -1}}})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

// LogWebhook stores a raw webhook body. Invalid JSON is kept as a string.
func (s *Store) LogWebhook(ctx context.Context, raw []byte, sigValid bool) error {
	doc := bson.M{"ts": time.Now(), "signature_valid": sigValid, "size": len(raw)}
	var body any
	if err := json.Unmarshal(raw, &body); err == nil {
		doc["body"] = body
	} else {
		doc["raw"] = string(raw)
	}
	_, err := s.db.Collection("webhook_events").InsertOne(ctx, doc)
	return err
}

// InsertMessage stores a handled message and returns its document ID.
func (s *Store) InsertMessage(ctx context.Context, r stats.Record) (string, error) {
	doc, err := toDoc(r)
	if err != nil {
		return "", err
	}
	id := bson.NewObjectID()
	doc["_id"] = id
	doc["ts"] = r.At
	if _, err := s.db.Collection("messages").InsertOne(ctx, doc); err != nil {
		return "", err
	}
	return id.Hex(), nil
}

// AddVerdicts merges shadow verdicts that finished after the reply.
func (s *Store) AddVerdicts(ctx context.Context, id string, vs map[string]decision.Verdict) error {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return err
	}
	set := bson.M{}
	for name, v := range vs {
		d, err := toDoc(v)
		if err != nil {
			return err
		}
		set["outcome.verdicts."+name] = d
	}
	_, err = s.db.Collection("messages").UpdateByID(ctx, oid, bson.M{"$set": set})
	return err
}

// AILog is one Gemini call.
type AILog struct {
	At           time.Time `json:"at"`
	MessageID    string    `json:"message_id"`
	UserID       string    `json:"user_id"`
	Source       string    `json:"source"`
	Model        string    `json:"model"`
	Decider      string    `json:"decider"` // decider that routed the message to AI
	Prompt       string    `json:"prompt"`
	Reply        string    `json:"reply"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	LatencyMs    int64     `json:"latency_ms"`
	Error        string    `json:"error,omitempty"`
}

func (s *Store) LogAI(ctx context.Context, l AILog) error {
	doc, err := toDoc(l)
	if err != nil {
		return err
	}
	doc["ts"] = l.At
	_, err = s.db.Collection("ai_logs").InsertOne(ctx, doc)
	return err
}

func (s *Store) SaveEval(ctx context.Context, rep decision.EvalReport) error {
	doc, err := toDoc(rep)
	if err != nil {
		return err
	}
	doc["ts"] = rep.At
	_, err = s.db.Collection("evals").InsertOne(ctx, doc)
	return err
}

// Messages returns messages since t (zero = all), oldest first, at most limit.
func (s *Store) Messages(ctx context.Context, since time.Time, limit int64) ([]stats.Record, error) {
	// Take the newest `limit` docs, then reverse so they replay oldest first.
	cur, err := s.db.Collection("messages").Find(ctx, sinceFilter(since),
		options.Find().SetSort(bson.D{{Key: "ts", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var out []stats.Record
	if err := decodeAll(ctx, cur, &out); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AILogs returns the newest AI calls since t.
func (s *Store) AILogs(ctx context.Context, since time.Time, limit int64) ([]AILog, error) {
	cur, err := s.db.Collection("ai_logs").Find(ctx, sinceFilter(since),
		options.Find().SetSort(bson.D{{Key: "ts", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var out []AILog
	return out, decodeAll(ctx, cur, &out)
}

// LatestEval returns the most recent eval run, or nil.
func (s *Store) LatestEval(ctx context.Context) (*decision.EvalReport, error) {
	var raw bson.M
	err := s.db.Collection("evals").FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "ts", Value: -1}})).Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rep decision.EvalReport
	return &rep, fromDoc(raw, &rep)
}

// Counts returns document counts per collection since t.
func (s *Store) Counts(ctx context.Context, since time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	for _, c := range []string{"webhook_events", "messages", "ai_logs", "evals"} {
		n, err := s.db.Collection(c).CountDocuments(ctx, sinceFilter(since))
		if err != nil {
			return nil, err
		}
		out[c] = n
	}
	return out, nil
}

func sinceFilter(t time.Time) bson.M {
	if t.IsZero() {
		return bson.M{}
	}
	return bson.M{"ts": bson.M{"$gte": t}}
}

// toDoc converts v to a BSON document with v's JSON field names.
func toDoc(v any) (bson.M, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var d bson.M
	return d, bson.UnmarshalExtJSON(j, false, &d)
}

// fromDoc converts a stored document back into v through JSON.
func fromDoc(d bson.M, v any) error {
	delete(d, "_id")
	delete(d, "ts")
	j, err := bson.MarshalExtJSON(d, false, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(j, v)
}

func decodeAll[T any](ctx context.Context, cur *mongo.Cursor, out *[]T) error {
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var raw bson.M
		if err := cur.Decode(&raw); err != nil {
			return err
		}
		var v T
		if err := fromDoc(raw, &v); err != nil {
			return err
		}
		*out = append(*out, v)
	}
	return cur.Err()
}
