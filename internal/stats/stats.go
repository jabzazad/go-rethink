// Package stats aggregates every message so deciders, routes, and costs can be compared.
package stats

import (
	"sort"
	"sync"
	"time"

	"poc-gateway/internal/decision"
	"poc-gateway/internal/gemini"
)

const (
	recentSize  = 100
	latencyKeep = 1000
)

// Record is one handled message.
type Record struct {
	ID        int64            `json:"id"`
	At        time.Time        `json:"at"`
	Source    string           `json:"source"` // "line", "line-menu", "line-test", "simulate"
	Text      string           `json:"text"`
	Expected  string           `json:"expected,omitempty"` // known correct label (rich menu tap / test question)
	Outcome   decision.Outcome `json:"outcome"`
	Responder string           `json:"responder"` // "bot" or "gemini"
	Gemini    *gemini.Result   `json:"gemini,omitempty"`
	Reply     string           `json:"reply"`
	LatencyMs int64            `json:"latency_ms"` // end to end: decide + answer
	Error     string           `json:"error,omitempty"`
}

// DeciderStats covers every verdict a decider gave, whether it drove the reply or ran as shadow.
type DeciderStats struct {
	Calls        int            `json:"calls"`
	Active       int            `json:"active"` // times its verdict drove the reply
	Errors       int            `json:"errors"`
	Abstains     int            `json:"abstains"`
	RouteBot     int            `json:"route_bot"`
	RouteAI      int            `json:"route_ai"`
	AgreeFinal   int            `json:"agree_final"` // same effective label as the final decision
	Compared     int            `json:"compared"`    // verdicts eligible for AgreeFinal
	GTTotal      int            `json:"gt_total"`    // verdicts on messages with a known answer
	GTCorrect    int            `json:"gt_correct"`
	InputTokens  int            `json:"input_tokens"`
	CostUSD      float64        `json:"cost_usd"`
	AvgLatencyMs float64        `json:"avg_latency_ms"`
	P50LatencyMs float64        `json:"p50_latency_ms"`
	P95LatencyMs float64        `json:"p95_latency_ms"`
	Labels       map[string]int `json:"labels"`

	latSumUs int64
	lat      []int64
}

// ABStats covers messages while a given decider was driving replies (swap mode).
type ABStats struct {
	Messages      int     `json:"messages"`
	BotReplies    int     `json:"bot_replies"`
	Fallbacks     int     `json:"fallbacks"` // slot was this decider but it failed
	GTTotal       int     `json:"gt_total"`
	GTCorrect     int     `json:"gt_correct"`
	DecisionUSD   float64 `json:"decision_usd"` // the driving decider only
	GeminiUSD     float64 `json:"gemini_usd"`
	AvgDecisionMs float64 `json:"avg_decision_ms"`
	AvgE2EMs      float64 `json:"avg_e2e_ms"`
	P50E2EMs      float64 `json:"p50_e2e_ms"`
	P95E2EMs      float64 `json:"p95_e2e_ms"`

	decSumUs int64
	e2eSumMs int64
	e2e      []int64
}

type GeminiStats struct {
	Calls        int     `json:"calls"`
	Errors       int     `json:"errors"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	latSumMs     int64
	billed       int // successful calls, used for the all-AI estimate
}

type Stats struct {
	mu        sync.Mutex
	started   time.Time
	nextID    int64
	total     int
	routes    map[string]int
	intents   map[string]int
	overrides int
	fallbacks int
	deciders  map[string]*DeciderStats
	ab        map[string]*ABStats
	gemini    GeminiStats
	recent    []Record
	lastEval  *decision.EvalReport
}

func New() *Stats { return NewSince(time.Now()) }

// NewSince is for rebuilding stats from stored records covering a window starting at t.
func NewSince(t time.Time) *Stats {
	return &Stats{
		started:  t,
		routes:   map[string]int{decision.RouteBot: 0, decision.RouteAI: 0},
		intents:  map[string]int{},
		deciders: map[string]*DeciderStats{},
		ab:       map[string]*ABStats{},
	}
}

func (s *Stats) abFor(name string) *ABStats {
	a := s.ab[name]
	if a == nil {
		a = &ABStats{}
		s.ab[name] = a
	}
	return a
}

// Add records a handled message and returns its ID (for AttachShadow).
func (s *Stats) Add(r Record) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	r.ID = s.nextID
	o := r.Outcome
	s.total++
	s.routes[o.Route]++
	if o.Route == decision.RouteBot && o.Label != "" {
		s.intents[o.Label]++
	}
	if o.Override != "" {
		s.overrides++
	}
	if o.FellBack {
		s.fallbacks++
		s.abFor(o.Slot).Fallbacks++
	}
	s.addVerdicts(r, o.Verdicts)

	// A/B: attribute the message to the decider that drove it.
	a := s.abFor(o.Active)
	a.Messages++
	if o.Route == decision.RouteBot {
		a.BotReplies++
	}
	if r.Expected != "" && o.Override == "" {
		a.GTTotal++
		if o.Label == r.Expected {
			a.GTCorrect++
		}
	}
	if v, ok := o.Verdicts[o.Active]; ok {
		a.DecisionUSD += v.CostUSD
		a.decSumUs += v.LatencyUs
	}
	if r.Gemini != nil {
		a.GeminiUSD += r.Gemini.CostUSD
	}
	a.e2eSumMs += r.LatencyMs
	a.e2e = appendCapped(a.e2e, r.LatencyMs)

	// Only count real Gemini calls (r.Gemini is nil when no key is configured).
	if r.Responder == "gemini" && r.Gemini != nil {
		g := &s.gemini
		g.Calls++
		if r.Error != "" {
			g.Errors++
		} else {
			g.billed++
		}
		g.InputTokens += r.Gemini.InputTokens
		g.OutputTokens += r.Gemini.OutputTokens
		g.CostUSD += r.Gemini.CostUSD
		g.latSumMs += r.Gemini.LatencyMs
	}
	s.recent = append(s.recent, r)
	if len(s.recent) > recentSize {
		s.recent = s.recent[len(s.recent)-recentSize:]
	}
	return r.ID
}

// AttachShadow adds verdicts that finished after the reply was sent.
func (s *Stats) AttachShadow(id int64, r Record, verdicts map[string]decision.Verdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addVerdicts(r, verdicts)
	for i := range s.recent {
		if s.recent[i].ID != id {
			continue
		}
		vs := make(map[string]decision.Verdict, len(s.recent[i].Outcome.Verdicts)+len(verdicts))
		for k, v := range s.recent[i].Outcome.Verdicts {
			vs[k] = v
		}
		for k, v := range verdicts {
			vs[k] = v
		}
		s.recent[i].Outcome.Verdicts = vs
	}
}

func (s *Stats) addVerdicts(r Record, verdicts map[string]decision.Verdict) {
	o := r.Outcome
	for name, v := range verdicts {
		d := s.deciders[name]
		if d == nil {
			d = &DeciderStats{Labels: map[string]int{}}
			s.deciders[name] = d
		}
		d.Calls++
		if name == o.Active {
			d.Active++
		}
		if v.Abstain {
			d.Abstains++
		}
		if v.Error != "" {
			d.Errors++
		} else {
			eff := v.EffectiveLabel()
			d.Labels[eff]++
			if v.Route() == decision.RouteBot {
				d.RouteBot++
			} else {
				d.RouteAI++
			}
			if o.Override == "" {
				d.Compared++
				if eff == o.Label {
					d.AgreeFinal++
				}
			}
		}
		if r.Expected != "" {
			d.GTTotal++
			if v.Error == "" && v.EffectiveLabel() == r.Expected {
				d.GTCorrect++
			}
		}
		d.InputTokens += v.InputTokens
		d.CostUSD += v.CostUSD
		d.latSumUs += v.LatencyUs
		d.lat = appendCapped(d.lat, v.LatencyUs)
	}
}

func (s *Stats) SetEval(rep decision.EvalReport) {
	s.mu.Lock()
	s.lastEval = &rep
	s.mu.Unlock()
}

type Cost struct {
	DecisionUSD float64 `json:"decision_usd"` // all decider calls (incl. shadow)
	GeminiUSD   float64 `json:"gemini_usd"`
	TotalUSD    float64 `json:"total_usd"`
	// AllAIUSD estimates the cost if every message had gone to Gemini
	// (average Gemini cost per call x total messages, no decider).
	AllAIUSD  float64 `json:"all_ai_usd"`
	SavedUSD  float64 `json:"saved_usd"`
	PerMsgUSD float64 `json:"per_msg_usd"`
	Estimated bool    `json:"estimated"` // false until at least one Gemini call happened
}

type Snapshot struct {
	Since     time.Time               `json:"since"`
	Total     int                     `json:"total"`
	Routes    map[string]int          `json:"routes"`
	BotShare  float64                 `json:"bot_share"`
	Intents   map[string]int          `json:"intents"`
	Overrides int                     `json:"overrides"`
	Fallbacks int                     `json:"fallbacks"`
	Deciders  map[string]DeciderStats `json:"deciders"`
	AB        map[string]ABStats      `json:"ab"`
	Gemini    GeminiStats             `json:"gemini"`
	Cost      Cost                    `json:"cost"`
	Recent    []Record                `json:"recent"`
	LastEval  *decision.EvalReport    `json:"last_eval,omitempty"`
}

func (s *Stats) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Since: s.started, Total: s.total, Overrides: s.overrides, Fallbacks: s.fallbacks,
		Routes: copyMap(s.routes), Intents: copyMap(s.intents),
		Deciders: map[string]DeciderStats{}, AB: map[string]ABStats{}, LastEval: s.lastEval,
	}
	if s.total > 0 {
		snap.BotShare = float64(s.routes[decision.RouteBot]) / float64(s.total)
	}
	for name, d := range s.deciders {
		c := *d
		c.Labels = copyMap(d.Labels)
		if d.Calls > 0 {
			c.AvgLatencyMs = float64(d.latSumUs) / float64(d.Calls) / 1000
		}
		c.P50LatencyMs, c.P95LatencyMs = percentile(d.lat, 0.50)/1000, percentile(d.lat, 0.95)/1000
		c.lat = nil
		snap.Deciders[name] = c
		snap.Cost.DecisionUSD += d.CostUSD
	}
	for name, a := range s.ab {
		c := *a
		if a.Messages > 0 {
			c.AvgDecisionMs = float64(a.decSumUs) / float64(a.Messages) / 1000
			c.AvgE2EMs = float64(a.e2eSumMs) / float64(a.Messages)
		}
		c.P50E2EMs, c.P95E2EMs = percentile(a.e2e, 0.50), percentile(a.e2e, 0.95)
		c.e2e = nil
		snap.AB[name] = c
	}
	snap.Gemini = s.gemini
	if s.gemini.Calls > 0 {
		snap.Gemini.AvgLatencyMs = float64(s.gemini.latSumMs) / float64(s.gemini.Calls)
	}
	c := &snap.Cost
	c.GeminiUSD = s.gemini.CostUSD
	c.TotalUSD = c.DecisionUSD + c.GeminiUSD
	if s.gemini.billed > 0 {
		c.AllAIUSD = s.gemini.CostUSD / float64(s.gemini.billed) * float64(s.total)
		c.SavedUSD = c.AllAIUSD - c.TotalUSD
		c.Estimated = true
	}
	if s.total > 0 {
		c.PerMsgUSD = c.TotalUSD / float64(s.total)
	}
	for i := len(s.recent) - 1; i >= 0; i-- { // newest first
		snap.Recent = append(snap.Recent, s.recent[i])
	}
	return snap
}

func appendCapped(xs []int64, x int64) []int64 {
	xs = append(xs, x)
	if len(xs) > latencyKeep {
		xs = xs[len(xs)-latencyKeep:]
	}
	return xs
}

// percentile returns the p-th value in the input's own unit.
func percentile(xs []int64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int64(nil), xs...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return float64(c[int(p*float64(len(c)-1))])
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
