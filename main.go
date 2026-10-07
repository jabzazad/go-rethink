// LINE webhook gateway: a PoC comparing two decision engines, Jev (TypeSafe, via
// Vercel AI Gateway) and Rethink (local, modelless), on accuracy, latency and cost.
//
// Each message is routed by the decision layer to a canned bot answer or to Gemini.
// In swap mode the decider driving replies alternates every SWAP_INTERVAL; the other
// one runs as an async shadow, so both are scored on the same traffic.
//
//	gateway                     run the server (dashboard at /)
//	gateway richmenu create     render, upload and set the default LINE rich menu
//	gateway richmenu preview f  write the rich menu image to f
//	gateway richmenu list       list rich menus
//	gateway richmenu delete id  delete a rich menu
package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"poc-gateway/internal/bot"
	"poc-gateway/internal/config"
	"poc-gateway/internal/decision"
	"poc-gateway/internal/gemini"
	"poc-gateway/internal/line"
	"poc-gateway/internal/richmenu"
	"poc-gateway/internal/stats"
	"poc-gateway/internal/store"
)

//go:embed dashboard.html
var dashboardHTML []byte

type server struct {
	channelSecret string
	line          *line.Client
	gemini        *gemini.Client // nil when GEMINI_API_KEY is unset
	pipeline      *decision.Pipeline
	stats         *stats.Stats
	store         *store.Store // nil when MONGO_URI is unset (memory only)
	info          map[string]any

	evalMu sync.Mutex
	eval   evalStatus
}

func main() {
	config.LoadDotEnv(".env")
	if len(os.Args) > 1 && os.Args[1] == "richmenu" {
		if err := richMenuCmd(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	ctx := context.Background()

	s := &server{
		channelSecret: os.Getenv("LINE_CHANNEL_SECRET"),
		line:          line.NewClient(os.Getenv("LINE_CHANNEL_ACCESS_TOKEN")),
		stats:         stats.New(),
	}

	// Responder: Gemini.
	geminiModel := config.String("GEMINI_MODEL", "gemini-2.5-flash")
	geminiPrice := gemini.Pricing{
		InputPerM:  config.Float("GEMINI_PRICE_INPUT_PER_M", 0.30),
		OutputPerM: config.Float("GEMINI_PRICE_OUTPUT_PER_M", 2.50),
	}
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		g, err := gemini.New(ctx, key, geminiModel, geminiPrice, int(config.Float("GEMINI_THINKING_BUDGET", 0)))
		if err != nil {
			log.Fatalf("gemini: %v", err)
		}
		s.gemini = g
	} else {
		log.Println("GEMINI_API_KEY not set: AI-routed messages get a placeholder reply")
	}

	// Deciders.
	rethinkMin := config.Float("RETHINK_MIN_CONFIDENCE", 0.65)
	rethink := decision.NewRethink(decision.Corpus, rethinkMin)
	keyword := decision.Keyword{MinStrength: config.Float("KEYWORD_MIN_STRENGTH", 0.3)}
	swapEvery, err := time.ParseDuration(config.String("SWAP_INTERVAL", "1m"))
	if err != nil {
		log.Fatalf("SWAP_INTERVAL: %v", err)
	}
	s.pipeline = &decision.Pipeline{
		Rethink:   rethink,
		Shadow:    []decision.Decider{keyword},
		Mode:      decision.Mode(config.String("DECIDER_MODE", string(decision.ModeSwap))),
		SwapEvery: swapEvery,
	}
	jevMin := config.Float("JEV_MIN_CONFIDENCE", 0.6)
	jevModel := config.String("JEV_MODEL", "typesafe-ai/jev")
	jevPrice := config.Float("JEV_PRICE_INPUT_PER_M", 0.042)
	// Jev via Vercel AI Gateway's Decision endpoint by default. For TypeSafe direct set
	// JEV_URL=https://api.typesafe.ai/v1/systemone and JEV_MODEL=jev-latest.
	if key := config.String("AI_GATEWAY_API_KEY", os.Getenv("JEV_API_KEY")); key != "" {
		s.pipeline.Jev = decision.Jev{
			APIKey:         key,
			URL:            config.String("JEV_URL", "https://ai-gateway.vercel.sh/v1/evaluate"),
			Model:          jevModel,
			MinConf:        jevMin,
			PricePerMInput: jevPrice,
			HTTP:           &http.Client{Timeout: time.Duration(config.Float("JEV_TIMEOUT_MS", 3000)) * time.Millisecond},
		}
		if rpm := int(config.Float("JEV_MAX_RPM", 0)); rpm > 0 {
			j := s.pipeline.Jev.(decision.Jev)
			j.Limiter = decision.NewRateLimiter(rpm)
			s.pipeline.Jev = j
		}
	} else {
		log.Println("AI_GATEWAY_API_KEY not set: Jev disabled, Rethink drives every reply")
	}
	if uri := os.Getenv("MONGO_URI"); uri != "" {
		st, err := store.New(ctx, uri, config.String("MONGO_DB", "decision_gateway"))
		if err != nil {
			log.Fatalf("mongo %s: %v", uri, err)
		}
		s.store = st
		log.Printf("storing webhooks, messages, AI logs and evals in MongoDB (%s)", uri)
	} else {
		log.Println("MONGO_URI not set: stats are in memory only and reset on restart")
	}
	if s.channelSecret == "" {
		log.Println("LINE_CHANNEL_SECRET not set: /webhook rejects everything (use the dashboard or /api/simulate)")
	}

	s.info = map[string]any{
		"mode": s.pipeline.Mode, "swap_interval_s": swapEvery.Seconds(), "shadow": []string{"keyword"},
		"jev_enabled": s.pipeline.Jev != nil, "jev_min_confidence": jevMin, "jev_price_input_per_m": jevPrice,
		"rethink_min_confidence": rethinkMin, "rethink_platt": map[string]float64{"a": rethink.A, "b": rethink.B},
		"rethink_corpus_size": len(decision.Corpus), "eval_set_size": len(decision.EvalSet),
		"keyword_min_strength": keyword.MinStrength,
		"gemini_enabled":       s.gemini != nil, "gemini_model": geminiModel, "gemini_price": geminiPrice,
		"labels": bot.Labels(), "menu_buttons": richmenu.Buttons,
		"mongo_enabled": s.store != nil,
		// The "jev" slot can run any decision model (e.g. Laya); label it by model.
		"jev_model": jevModel, "jev_label": jevLabel(jevModel), "jev_max_rpm": config.Float("JEV_MAX_RPM", 0),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(dashboardHTML)
	})
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("POST /api/simulate", s.handleSimulate)
	mux.HandleFunc("POST /api/eval", s.handleEval)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/ai-logs", s.handleAILogs)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// Everything except the LINE webhook (signature-protected) and healthz needs the
	// dashboard password, since the tunnel makes this server public.
	handler := http.Handler(mux)
	if pass := os.Getenv("DASHBOARD_PASSWORD"); pass != "" {
		user := config.String("DASHBOARD_USER", "team")
		handler = basicAuth(mux, user, pass)
	} else {
		log.Println("WARNING: DASHBOARD_PASSWORD not set: the dashboard and /api/* are open to anyone who can reach this server")
	}

	addr := ":" + config.String("PORT", "8080")
	log.Printf("listening on %s  dashboard: http://localhost%s/  mode=%s swap=%s jev=%v", addr, addr, s.pipeline.Mode, swapEvery, s.pipeline.Jev != nil)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	valid := s.channelSecret != "" && line.VerifySignature(s.channelSecret, body, r.Header.Get("X-Line-Signature"))
	if s.store != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.store.LogWebhook(ctx, body, valid); err != nil {
				log.Printf("mongo webhook log: %v", err)
			}
		}()
	}
	if !valid {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var wb line.WebhookBody
	if err := json.Unmarshal(body, &wb); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// LINE wants a fast 200; the AI can take seconds, so reply asynchronously.
	w.WriteHeader(http.StatusOK)
	for _, ev := range wb.Events {
		var run func(ctx context.Context) stats.Record
		switch {
		case ev.Type == "message" && ev.Message != nil && ev.Message.Type == "text":
			text := ev.Message.Text
			run = func(ctx context.Context) stats.Record {
				// A rich menu tap sends a known text, so the right answer is known.
				if exp, ok := richmenu.Expected(text); ok {
					return s.handle(ctx, "line-menu", ev.Source.UserID, text, exp)
				}
				return s.handle(ctx, "line", ev.Source.UserID, text, "")
			}
		case ev.Type == "postback" && ev.Postback != nil && ev.Postback.Data == richmenu.PostbackTest:
			run = func(ctx context.Context) stats.Record { return s.handleTest(ctx, "line-test", ev.Source.UserID) }
		default:
			continue
		}
		go func(token string) {
			// Reply tokens expire after about a minute.
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			rec := run(ctx)
			if err := s.line.ReplyText(ctx, token, rec.Reply); err != nil {
				log.Printf("line reply failed: %v", err)
			}
		}(ev.ReplyToken)
	}
}

// handleTest answers a random held-out question and shows whether the decider got it right.
func (s *server) handleTest(ctx context.Context, source, userID string) stats.Record {
	ex := decision.EvalSet[rand.IntN(len(decision.EvalSet))]
	rec := s.handle(ctx, source, userID, ex.Text, ex.Label)
	mark := "✅"
	if rec.Outcome.Label != ex.Label {
		mark = "❌"
	}
	rec.Reply = fmt.Sprintf("🧪 คำถามทดสอบ: %s\nคาดหวัง: %s · ได้: %s (%s) %s\n\n%s",
		ex.Text, ex.Label, rec.Outcome.Label, rec.Outcome.Active, mark, rec.Reply)
	return rec
}

// handle decides, answers, and records one message. expected is the known correct
// label, or "" when unknown.
func (s *server) handle(ctx context.Context, source, userID, text, expected string) stats.Record {
	start := time.Now()
	var id int64
	var docID string
	ready := make(chan struct{})
	var base stats.Record
	out := s.pipeline.Decide(ctx, text, func(vs map[string]decision.Verdict) {
		<-ready // the record must exist before shadow verdicts attach to it
		s.stats.AttachShadow(id, base, vs)
		if s.store != nil && docID != "" {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.store.AddVerdicts(sctx, docID, vs); err != nil {
				log.Printf("mongo verdicts: %v", err)
			}
		}
	})
	rec := stats.Record{At: start, Source: source, Text: text, Expected: expected, Outcome: out}

	if out.Route == decision.RouteBot {
		rec.Responder = "bot"
		rec.Reply = bot.Answer(out.Label)
	} else {
		rec.Responder = "gemini"
		if s.gemini == nil {
			rec.Reply = "(AI route: set GEMINI_API_KEY in .env to get a real answer)"
			rec.Error = "gemini not configured"
		} else {
			res, err := s.gemini.Reply(ctx, userID, out.Text)
			rec.Gemini = &res
			rec.Reply = res.Text
			if err != nil {
				log.Printf("gemini error: %v", err)
				rec.Error = err.Error()
				rec.Reply = "ขออภัยครับ ระบบ AI ขัดข้องชั่วคราว ลองใหม่อีกครั้ง หรือพิมพ์ \"เมนู\" ครับ"
			}
		}
	}
	rec.LatencyMs = time.Since(start).Milliseconds()
	id = s.stats.Add(rec)
	base = rec
	if s.store != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var err error
		if docID, err = s.store.InsertMessage(sctx, rec); err != nil {
			log.Printf("mongo message: %v", err)
		}
		if rec.Gemini != nil {
			err := s.store.LogAI(sctx, store.AILog{
				At: start, MessageID: docID, UserID: userID, Source: source, Model: s.gemini.Model(),
				Decider: out.Active, Prompt: out.Text, Reply: rec.Reply,
				InputTokens: rec.Gemini.InputTokens, OutputTokens: rec.Gemini.OutputTokens,
				CostUSD: rec.Gemini.CostUSD, LatencyMs: rec.Gemini.LatencyMs, Error: rec.Error,
			})
			if err != nil {
				log.Printf("mongo ai log: %v", err)
			}
		}
		cancel()
	}
	close(ready)
	log.Printf("src=%s route=%s label=%q expected=%q active=%s slot=%s fellback=%v latency=%dms",
		source, out.Route, out.Label, expected, out.Active, out.Slot, out.FellBack, rec.LatencyMs)
	rec.ID = id
	return rec
}

// handleSimulate tests the full flow without LINE:
//
//	curl -X POST localhost:8080/api/simulate -d '{"text":"ราคาเท่าไหร่"}'
//	{"test":true} answers a random eval question; {"text":...,"expected":"price"} scores it.
func (s *server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID   string `json:"user_id"`
		Text     string `json:"text"`
		Expected string `json:"expected"`
		Test     bool   `json:"test"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Text == "" && !in.Test) {
		http.Error(w, `body must be {"text":"..."} or {"test":true}`, http.StatusBadRequest)
		return
	}
	if in.UserID == "" {
		in.UserID = "simulator"
	}
	if in.Test {
		writeJSON(w, s.handleTest(r.Context(), "simulate-test", in.UserID))
		return
	}
	if in.Expected == "" {
		in.Expected, _ = richmenu.Expected(in.Text)
	}
	writeJSON(w, s.handle(r.Context(), "simulate", in.UserID, in.Text, in.Expected))
}

// handleEval starts a background run of the labelled eval set through every decider
// (no Gemini calls). It can take minutes when a decider is rate limited, so the
// dashboard polls /api/stats for progress.
func (s *server) handleEval(w http.ResponseWriter, _ *http.Request) {
	s.evalMu.Lock()
	if s.eval.Running {
		s.evalMu.Unlock()
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{"running": true})
		return
	}
	s.eval = evalStatus{Running: true}
	s.evalMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		rep := decision.RunEval(ctx, s.pipeline.All(), decision.EvalSet, func(done, total int) {
			s.evalMu.Lock()
			s.eval.Done, s.eval.Total = done, total
			s.evalMu.Unlock()
		})
		s.stats.SetEval(rep)
		if s.store != nil {
			if err := s.store.SaveEval(ctx, rep); err != nil {
				log.Printf("mongo eval: %v", err)
			}
		}
		s.evalMu.Lock()
		s.eval.Running = false
		s.evalMu.Unlock()
		log.Printf("eval done")
	}()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"started": true})
}

type evalStatus struct {
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`
}

// ranges maps the dashboard's range picker to a lookback ("all" = zero).
var ranges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "all": 0}

func sinceFor(r *http.Request) (time.Time, string) {
	key := r.URL.Query().Get("range")
	d, ok := ranges[key]
	if !ok {
		key, d = "all", 0
	}
	if d == 0 {
		return time.Time{}, key
	}
	return time.Now().Add(-d), key
}

// handleStats serves the dashboard. With Mongo it rebuilds the stats from the stored
// messages in the selected range (so they survive restarts); otherwise it uses memory.
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	sched, end := s.pipeline.Scheduled(time.Now())
	now := map[string]any{"scheduled": sched.Name()}
	if !end.IsZero() {
		now["slot_ends_in_s"] = time.Until(end).Seconds()
	}
	s.evalMu.Lock()
	resp := map[string]any{"config": s.info, "now": now, "eval": s.eval}
	s.evalMu.Unlock()
	if s.store == nil {
		resp["source"] = "memory"
		resp["stats"] = s.stats.Snapshot()
		writeJSON(w, resp)
		return
	}
	since, key := sinceFor(r)
	const limit = 50000
	recs, err := s.store.Messages(r.Context(), since, limit)
	if err != nil {
		http.Error(w, "mongo: "+err.Error(), http.StatusBadGateway)
		return
	}
	start := since
	if start.IsZero() && len(recs) > 0 {
		start = recs[0].At
	}
	st := stats.NewSince(start)
	for _, rec := range recs {
		st.Add(rec)
	}
	if ev, err := s.store.LatestEval(r.Context()); err == nil && ev != nil {
		st.SetEval(*ev)
	}
	counts, _ := s.store.Counts(r.Context(), since)
	resp["source"] = "mongo"
	resp["range"] = key
	resp["stored"] = counts
	resp["truncated"] = len(recs) == limit
	resp["stats"] = st.Snapshot()
	writeJSON(w, resp)
}

func (s *server) handleAILogs(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, []store.AILog{})
		return
	}
	since, _ := sinceFor(r)
	logs, err := s.store.AILogs(r.Context(), since, 50)
	if err != nil {
		http.Error(w, "mongo: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, logs)
}

func richMenuCmd(args []string) error {
	ctx := context.Background()
	cli := line.NewClient(os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"))
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "preview":
		if len(args) < 2 {
			return fmt.Errorf("usage: gateway richmenu preview out.png")
		}
		img, err := richmenu.Render(fontPath())
		if err != nil {
			return err
		}
		return os.WriteFile(args[1], img, 0o644)
	case "create":
		if os.Getenv("LINE_CHANNEL_ACCESS_TOKEN") == "" {
			return fmt.Errorf("LINE_CHANNEL_ACCESS_TOKEN is not set")
		}
		img, ctype, err := menuImage()
		if err != nil {
			return err
		}
		id, err := cli.CreateRichMenu(ctx, richmenu.Menu())
		if err != nil {
			return err
		}
		if err := cli.UploadRichMenuImage(ctx, id, ctype, img); err != nil {
			return err
		}
		if err := cli.SetDefaultRichMenu(ctx, id); err != nil {
			return err
		}
		fmt.Println("created and set as default:", id)
		return nil
	case "list":
		ids, err := cli.ListRichMenus(ctx)
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(ids, "\n"))
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: gateway richmenu delete <richMenuId>")
		}
		return cli.DeleteRichMenu(ctx, args[1])
	}
	return fmt.Errorf("usage: gateway richmenu create|preview <file>|list|delete <id>")
}

// menuImage uses RICHMENU_IMAGE if set, otherwise renders one.
func menuImage() ([]byte, string, error) {
	if p := os.Getenv("RICHMENU_IMAGE"); p != "" {
		b, err := os.ReadFile(p)
		ctype := "image/png"
		if ext := strings.ToLower(filepath.Ext(p)); ext == ".jpg" || ext == ".jpeg" {
			ctype = "image/jpeg"
		}
		return b, ctype, err
	}
	b, err := richmenu.Render(fontPath())
	return b, "image/png", err
}

func fontPath() string {
	return config.String("RICHMENU_FONT", `C:\Windows\Fonts\LeelawUI.ttf`)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// jevLabel turns "convaiinnovations/laya" into "laya" for display.
func jevLabel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.TrimSuffix(model, "-free")
}

func basicAuth(next http.Handler, user, pass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhook" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		okUser := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		okPass := subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
		if !ok || !okUser || !okPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="decision gateway"`)
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
