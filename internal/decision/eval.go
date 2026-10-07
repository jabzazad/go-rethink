package decision

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type EvalItem struct {
	Text     string  `json:"text"`
	Expected string  `json:"expected"`
	Got      string  `json:"got"` // effective label (abstain => ai)
	Correct  bool    `json:"correct"`
	Verdict  Verdict `json:"verdict"`
}

type EvalScore struct {
	Decider  string `json:"decider"`
	N        int    `json:"n"`
	Answered int    `json:"answered"` // N minus errors; accuracy and latency are over these
	// LabelAccuracy: exact label right (abstain counts as "ai").
	LabelAccuracy float64 `json:"label_accuracy"`
	// RouteAccuracy: bot-vs-ai right, ignoring which intent.
	RouteAccuracy float64 `json:"route_accuracy"`
	// WrongBot counts messages that should have gone to AI but got a canned
	// answer: the costly failure, since the user gets an irrelevant reply.
	WrongBot     int        `json:"wrong_bot"`
	Abstains     int        `json:"abstains"`
	Errors       int        `json:"errors"`
	AvgLatencyMs float64    `json:"avg_latency_ms"`
	P50LatencyMs float64    `json:"p50_latency_ms"`
	P95LatencyMs float64    `json:"p95_latency_ms"`
	TotalCostUSD float64    `json:"total_cost_usd"`
	Items        []EvalItem `json:"items"`
}

type EvalReport struct {
	At     time.Time   `json:"at"`
	Scores []EvalScore `json:"scores"`
}

// RunEval runs every example through every decider (at most 4 in flight per decider).
// Rate-limited deciders wait for slots instead of failing. progress, if set, is called
// after each item with (done, total).
func RunEval(ctx context.Context, ds []Decider, set []Example, progress func(done, total int)) EvalReport {
	ctx = WithPaced(ctx)
	rep := EvalReport{At: time.Now()}
	total := len(ds) * len(set)
	var done atomic.Int64
	for _, d := range ds {
		items := make([]EvalItem, len(set))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, ex := range set {
			wg.Add(1)
			go func(i int, ex Example) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				v := d.Decide(ctx, ex.Text)
				if progress != nil {
					progress(int(done.Add(1)), total)
				}
				got := v.EffectiveLabel()
				items[i] = EvalItem{Text: ex.Text, Expected: ex.Label, Got: got, Correct: got == ex.Label, Verdict: v}
			}(i, ex)
		}
		wg.Wait()

		// Latency per item in ms. Network deciders: the measured call. Fast local
		// deciders: the mean over many repetitions, because a single sub-millisecond
		// call is below the OS clock resolution (about 0.5 ms on Windows).
		lat := make([]float64, len(items))
		var sumUs int64
		for i, it := range items {
			lat[i] = float64(it.Verdict.LatencyUs) / 1000
			sumUs += it.Verdict.LatencyUs
		}
		if len(items) > 0 && sumUs/int64(len(items)) < 5000 {
			for i, ex := range set {
				const reps = 200
				t0 := time.Now()
				for r := 0; r < reps; r++ {
					d.Decide(ctx, ex.Text)
				}
				lat[i] = float64(time.Since(t0).Nanoseconds()) / reps / 1e6
			}
		}

		// Accuracy and latency count answered calls only; errors are reported
		// separately, so an outage doesn't read as "chose ai".
		s := EvalScore{Decider: d.Name(), N: len(items), Items: items}
		var labelOK, routeOK int
		var answeredLat []float64
		for i, it := range items {
			s.TotalCostUSD += it.Verdict.CostUSD
			if it.Verdict.Error != "" {
				s.Errors++
				continue
			}
			s.Answered++
			answeredLat = append(answeredLat, lat[i])
			if it.Correct {
				labelOK++
			}
			expRoute := RouteBot
			if it.Expected == "ai" {
				expRoute = RouteAI
			}
			if it.Verdict.Route() == expRoute {
				routeOK++
			} else if expRoute == RouteAI {
				s.WrongBot++
			}
			if it.Verdict.Abstain {
				s.Abstains++
			}
		}
		if n := float64(s.Answered); n > 0 {
			s.LabelAccuracy = float64(labelOK) / n
			s.RouteAccuracy = float64(routeOK) / n
			var sum float64
			for _, l := range answeredLat {
				sum += l
			}
			s.AvgLatencyMs = sum / n
			sort.Float64s(answeredLat)
			s.P50LatencyMs = answeredLat[len(answeredLat)/2]
			s.P95LatencyMs = answeredLat[int(0.95*float64(len(answeredLat)-1))]
		}
		rep.Scores = append(rep.Scores, s)
	}
	sort.Slice(rep.Scores, func(i, j int) bool { return rep.Scores[i].LabelAccuracy > rep.Scores[j].LabelAccuracy })
	return rep
}
