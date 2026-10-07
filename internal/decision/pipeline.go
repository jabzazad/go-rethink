package decision

import (
	"context"
	"strings"
	"sync"
	"time"

	"poc-gateway/internal/bot"
)

// Outcome is the final routing decision plus the verdicts gathered so far.
type Outcome struct {
	Route    string             `json:"route"`
	Label    string             `json:"label"`
	Active   string             `json:"active"`              // decider whose verdict was used
	Slot     string             `json:"slot"`                // decider scheduled for this time slot
	FellBack bool               `json:"fell_back,omitempty"` // scheduled decider failed, fallback used
	Override string             `json:"override,omitempty"`  // "/ai" or "/bot" prefix
	Text     string             `json:"-"`                   // text with any prefix stripped
	Verdicts map[string]Verdict `json:"verdicts"`
}

// Mode chooses which decider drives the reply.
type Mode string

const (
	ModeSwap    Mode = "swap"    // alternate Jev and Rethink every SwapEvery
	ModeJev     Mode = "jev"     // always Jev (Rethink on error)
	ModeRethink Mode = "rethink" // always Rethink
)

// Pipeline runs the scheduled decider synchronously (it drives the reply) and
// every other decider as an asynchronous shadow, so comparisons never slow the reply.
type Pipeline struct {
	Jev       Decider // nil when no key
	Rethink   Decider
	Shadow    []Decider // extra baselines, e.g. keyword
	Mode      Mode
	SwapEvery time.Duration
}

func (p *Pipeline) All() []Decider {
	var out []Decider
	for _, d := range append([]Decider{p.Jev, p.Rethink}, p.Shadow...) {
		if d != nil {
			out = append(out, d)
		}
	}
	return out
}

// Scheduled returns the decider for time t and when the slot ends.
func (p *Pipeline) Scheduled(t time.Time) (Decider, time.Time) {
	if p.Jev == nil || p.Mode == ModeRethink {
		return p.Rethink, time.Time{}
	}
	if p.Mode == ModeJev || p.SwapEvery <= 0 {
		return p.Jev, time.Time{}
	}
	slot := t.UnixNano() / int64(p.SwapEvery)
	end := time.Unix(0, (slot+1)*int64(p.SwapEvery))
	if slot%2 == 0 {
		return p.Jev, end
	}
	return p.Rethink, end
}

// Decide returns as soon as the scheduled decider answers. onShadow, if set, is
// called later (from another goroutine) with the remaining deciders' verdicts.
func (p *Pipeline) Decide(ctx context.Context, text string, onShadow func(map[string]Verdict)) Outcome {
	text = strings.TrimSpace(text)
	out := Outcome{Text: text, Verdicts: map[string]Verdict{}}

	// Explicit prefixes skip the decision, handy for demos.
	if rest, ok := cutPrefix(text, "/ai"); ok {
		out.Text, out.Override = rest, "/ai"
	} else if rest, ok := cutPrefix(text, "/bot"); ok {
		out.Text, out.Override = rest, "/bot"
	}

	sched, _ := p.Scheduled(time.Now())
	out.Slot = sched.Name()
	v := sched.Decide(ctx, out.Text)
	out.Verdicts[sched.Name()] = v
	active := sched
	if v.Error != "" && sched != p.Rethink {
		out.FellBack = true
		active = p.Rethink
		v = p.Rethink.Decide(ctx, out.Text)
		out.Verdicts[p.Rethink.Name()] = v
	}
	out.Active = active.Name()
	out.Route, out.Label = v.Route(), v.EffectiveLabel()

	switch out.Override {
	case "/ai":
		out.Route, out.Label = RouteAI, bot.LabelAI
	case "/bot":
		out.Route = RouteBot
		if v.Label != bot.LabelAI {
			out.Label = v.Label
		} else {
			out.Label = ""
		}
	}

	var rest []Decider
	for _, d := range p.All() {
		if _, done := out.Verdicts[d.Name()]; !done {
			rest = append(rest, d)
		}
	}
	if len(rest) > 0 && onShadow != nil {
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			onShadow(runAll(sctx, rest, out.Text))
		}()
	}
	return out
}

func runAll(ctx context.Context, ds []Decider, text string) map[string]Verdict {
	res := make(map[string]Verdict, len(ds))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range ds {
		wg.Add(1)
		go func(d Decider) {
			defer wg.Done()
			v := d.Decide(ctx, text)
			mu.Lock()
			res[d.Name()] = v
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	return res
}

func cutPrefix(text, prefix string) (string, bool) {
	lower := strings.ToLower(text)
	if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
		return strings.TrimSpace(text[len(prefix):]), true
	}
	return "", false
}
