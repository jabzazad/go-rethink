package decision

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestKeyword(t *testing.T) {
	k := Keyword{MinStrength: 0.3}
	cases := map[string]string{
		"สวัสดีครับ":   "greeting",
		"ราคาเท่าไหร่": "price",
		"อยากรู้ว่าราคาติดตั้งแอร์ 2 เครื่องพร้อมเดินท่อใหม่ทั้งหมดกับเปลี่ยนเบรกเกอร์ ประมาณเท่าไหร่": "ai",
		"ผนังร้าว อันตรายไหม": "ai",
	}
	for text, want := range cases {
		if got := k.Decide(context.Background(), text).EffectiveLabel(); got != want {
			t.Errorf("keyword(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestRethinkCalibratedAndAccurate(t *testing.T) {
	r := NewRethink(Corpus, 0.65)
	if r.A <= 0 {
		t.Fatalf("Platt slope must be positive, got A=%.2f B=%.2f", r.A, r.B)
	}
	rep := RunEval(context.Background(), []Decider{r}, EvalSet, nil)
	s := rep.Scores[0]
	t.Logf("rethink: label acc %.2f, route acc %.2f, wrong-bot %d, abstains %d (A=%.2f B=%.2f)",
		s.LabelAccuracy, s.RouteAccuracy, s.WrongBot, s.Abstains, r.A, r.B)
	for _, it := range s.Items {
		if !it.Correct {
			t.Logf("  miss: %q want %s got %s (%s)", it.Text, it.Expected, it.Got, it.Verdict.Reason)
		}
	}
	if s.RouteAccuracy < 0.75 {
		t.Errorf("rethink route accuracy %.2f < 0.75 on eval set", s.RouteAccuracy)
	}
}

func jevServer(t *testing.T, fail bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req jevRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Questions["route"].Type != "choice" || len(req.Questions["route"].Criteria) != 8 {
			http.Error(w, "bad question", http.StatusBadRequest)
			return
		}
		// Vercel /v1/evaluate shape: camelCase, no confidence on choice answers.
		w.Write([]byte(`{"model":"typesafe-ai/jev","answers":{"route":{"type":"choice","choice":"hours","probabilities":{"hours":0.9,"ai":0.1}}},"usage":{"inputTokens":1000,"outputTokens":20},"providerMetadata":{"gateway":{"cost":"0.0000421"}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPipelineFallsBackWhenJevFails(t *testing.T) {
	srv := jevServer(t, true)
	p := &Pipeline{
		Jev:     Jev{APIKey: "key", URL: srv.URL, Model: "typesafe-ai/jev", MinConf: 0.6, HTTP: srv.Client()},
		Rethink: NewRethink(Corpus, 0.65),
		Shadow:  []Decider{Keyword{MinStrength: 0.3}},
		Mode:    ModeJev,
	}
	shadow := make(chan map[string]Verdict, 1)
	out := p.Decide(context.Background(), "ราคาเท่าไหร่", func(v map[string]Verdict) { shadow <- v })
	if !out.FellBack || out.Active != "rethink" || out.Slot != "jev" {
		t.Fatalf("want fallback to rethink, got active=%s slot=%s fellBack=%v", out.Active, out.Slot, out.FellBack)
	}
	if out.Label != "price" {
		t.Errorf("got label %q", out.Label)
	}
	if v := <-shadow; len(v) != 1 || v["keyword"].Label != "price" {
		t.Errorf("shadow verdicts = %+v", v)
	}
}

func TestPipelineUsesJevAndShadowsRethink(t *testing.T) {
	srv := jevServer(t, false)
	p := &Pipeline{
		Jev:     Jev{APIKey: "key", URL: srv.URL, Model: "typesafe-ai/jev", MinConf: 0.6, PricePerMInput: 0.042, HTTP: srv.Client()},
		Rethink: NewRethink(Corpus, 0.65),
		Mode:    ModeJev,
	}
	shadow := make(chan map[string]Verdict, 1)
	out := p.Decide(context.Background(), "เปิดกี่โมง", func(v map[string]Verdict) { shadow <- v })
	v := out.Verdicts["jev"]
	if out.Active != "jev" || out.Label != "hours" || out.Route != RouteBot {
		t.Fatalf("got %+v (jev verdict %+v)", out, v)
	}
	if v.Confidence != 0.9 || v.InputTokens != 1000 {
		t.Errorf("confidence %v (want chosen option's probability 0.9), tokens %d", v.Confidence, v.InputTokens)
	}
	if math.Abs(v.CostUSD-0.0000421) > 1e-12 { // gateway-billed cost wins over the computed one
		t.Errorf("cost = %v", v.CostUSD)
	}
	if sv := <-shadow; sv["rethink"].Label != "hours" {
		t.Errorf("rethink shadow = %+v", sv["rethink"])
	}
}

func TestJevParsesTypeSafeShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"model":"jev-1.13.0","answers":{"route":{"type":"choice","choice":"price","probabilities":{"price":0.7,"ai":0.3},"confidence":0.55}},"usage":{"input_tokens":500,"output_tokens":20}}`))
	}))
	defer srv.Close()
	v := Jev{APIKey: "k", URL: srv.URL, MinConf: 0.6, PricePerMInput: 0.042, HTTP: srv.Client()}.Decide(context.Background(), "x")
	// Explicit confidence (0.55) wins over the probability (0.7), and is below the floor.
	if v.Label != "price" || v.Confidence != 0.55 || !v.Abstain || v.InputTokens != 500 {
		t.Errorf("got %+v", v)
	}
	if math.Abs(v.CostUSD-500/1e6*0.042) > 1e-12 {
		t.Errorf("computed cost = %v", v.CostUSD)
	}
}

func TestRateLimiter(t *testing.T) {
	l := NewRateLimiter(2)
	if !l.TryAcquire() || !l.TryAcquire() || l.TryAcquire() {
		t.Fatal("want exactly 2 slots per minute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait should block until the window frees, not succeed immediately")
	}
}

func TestJevFailsFastWhenOverLimit(t *testing.T) {
	srv := jevServer(t, false)
	j := Jev{APIKey: "key", URL: srv.URL, MinConf: 0.6, HTTP: srv.Client(), Limiter: NewRateLimiter(1)}
	if v := j.Decide(context.Background(), "x"); v.Error != "" {
		t.Fatalf("first call should pass: %s", v.Error)
	}
	if v := j.Decide(context.Background(), "x"); v.Error == "" {
		t.Fatal("second live call in the same minute should fail fast")
	}
}

func TestSwapSchedule(t *testing.T) {
	p := &Pipeline{Jev: Keyword{}, Rethink: NewRethink(Corpus, 0.65), Mode: ModeSwap, SwapEvery: time.Minute}
	base := time.Unix(0, 0)
	if d, _ := p.Scheduled(base.Add(30 * time.Second)); d.Name() != "keyword" { // slot 0 = Jev's slot
		t.Errorf("slot 0 = %s", d.Name())
	}
	if d, end := p.Scheduled(base.Add(90 * time.Second)); d.Name() != "rethink" || !end.Equal(base.Add(2*time.Minute)) {
		t.Errorf("slot 1 = %s ending %v", d.Name(), end)
	}
	p.Jev = nil
	if d, _ := p.Scheduled(base); d.Name() != "rethink" {
		t.Errorf("no jev should always be rethink, got %s", d.Name())
	}
}

func TestOverride(t *testing.T) {
	p := &Pipeline{Rethink: NewRethink(Corpus, 0.65)}
	if out := p.Decide(context.Background(), "/ai สวัสดี", nil); out.Route != RouteAI || out.Text != "สวัสดี" {
		t.Errorf("/ai override: %+v", out)
	}
}
