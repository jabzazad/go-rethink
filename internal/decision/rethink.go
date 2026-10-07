package decision

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"poc-gateway/internal/bot"
)

// Rethink is a modelless decision engine following the "rethink" pattern from
// katgpt (https://github.com/katopz/katgpt-rs, see its Research 562 and 576):
// modelless corpus nearest-neighbour, Platt-calibrated confidence, and abstain
// instead of guess. This is an independent Go implementation of the publicly
// documented pattern, not katgpt's own code.
//
// Concretely:
// no trained heads, only nearest neighbours in a labelled corpus; the raw
// similarity is mapped to a probability by a 2-parameter Platt fit
// (p = sigmoid(A*sim + B)) learned leave-one-out from the corpus itself;
// and when p is below MinConf it abstains instead of guessing, which
// escalates the message to the AI.
//
// Similarity is cosine over character 2/3-grams, which works for Thai
// (no spaces between words) without a tokenizer.
type Rethink struct {
	MinConf float64
	K       int

	corpus []vec
	labels []string
	texts  []string
	// Platt parameters.
	A, B float64
}

type vec struct {
	grams map[string]float64
	norm  float64
}

// NewRethink indexes the corpus and fits calibration.
func NewRethink(corpus []Example, minConf float64) *Rethink {
	r := &Rethink{MinConf: minConf, K: 3}
	for _, ex := range corpus {
		r.corpus = append(r.corpus, vectorize(ex.Text))
		r.labels = append(r.labels, ex.Label)
		r.texts = append(r.texts, ex.Text)
	}
	r.fit()
	return r
}

func (Rethink) Name() string { return "rethink" }

func (r *Rethink) Decide(_ context.Context, text string) Verdict {
	start := time.Now()
	label, sim, nearest := r.predict(vectorize(text), -1)
	p := sigmoid(r.A*sim + r.B)
	v := Verdict{Decider: r.Name(), Label: label, Confidence: p}
	switch {
	case label == bot.LabelAI:
		v.Reason = fmt.Sprintf("nearest examples are ai (sim %.2f, p %.2f) e.g. %q", sim, p, truncate(nearest, 40))
	case p < r.MinConf:
		v.Abstain = true
		v.Reason = fmt.Sprintf("closest is %q but p %.2f < %.2f (sim %.2f): abstain, escalate", label, p, r.MinConf, sim)
	default:
		v.Reason = fmt.Sprintf("nearest %q (sim %.2f, p %.2f) e.g. %q", label, sim, p, truncate(nearest, 40))
	}
	v.LatencyUs = time.Since(start).Microseconds()
	return v
}

// predict does a similarity-weighted vote over the top K neighbours.
// skip excludes one corpus index (for leave-one-out fitting).
func (r *Rethink) predict(q vec, skip int) (label string, topSim float64, nearest string) {
	type hit struct {
		i   int
		sim float64
	}
	hits := make([]hit, 0, len(r.corpus))
	for i, c := range r.corpus {
		if i == skip {
			continue
		}
		hits = append(hits, hit{i, cosine(q, c)})
	}
	if len(hits) == 0 {
		return bot.LabelAI, 0, ""
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].sim > hits[b].sim })
	votes := map[string]float64{}
	for _, h := range hits[:min(r.K, len(hits))] {
		votes[r.labels[h.i]] += h.sim
	}
	best, bestW := bot.LabelAI, -1.0
	for l, w := range votes {
		if w > bestW || (w == bestW && l < best) {
			best, bestW = l, w
		}
	}
	// Report the similarity of the closest neighbour that carries the winning label.
	for _, h := range hits {
		if r.labels[h.i] == best {
			return best, h.sim, r.texts[h.i]
		}
	}
	return best, hits[0].sim, r.texts[hits[0].i]
}

// fit learns Platt parameters by logistic regression on leave-one-out
// (similarity, was-correct) pairs from the corpus.
func (r *Rethink) fit() {
	r.A, r.B = 10, -4 // sane default if the corpus is too small to fit
	type pt struct{ x, y float64 }
	var pts []pt
	for i := range r.corpus {
		l, sim, _ := r.predict(r.corpus[i], i)
		y := 0.0
		if l == r.labels[i] {
			y = 1
		}
		pts = append(pts, pt{sim, y})
	}
	var pos int
	for _, p := range pts {
		pos += int(p.y)
	}
	if len(pts) < 10 || pos == 0 || pos == len(pts) {
		return
	}
	a, b := r.A, r.B
	lr := 0.5
	for iter := 0; iter < 5000; iter++ {
		var ga, gb float64
		for _, p := range pts {
			d := sigmoid(a*p.x+b) - p.y
			ga += d * p.x
			gb += d
		}
		n := float64(len(pts))
		a -= lr * ga / n
		b -= lr * gb / n
	}
	// A must stay positive so higher similarity never means lower confidence.
	if a > 0 && !math.IsNaN(a) && !math.IsNaN(b) {
		r.A, r.B = a, b
	}
}

func vectorize(s string) vec {
	var b strings.Builder
	space := true
	for _, ch := range strings.ToLower(s) {
		if unicode.IsLetter(ch) || unicode.IsNumber(ch) || unicode.Is(unicode.Mn, ch) {
			b.WriteRune(ch)
			space = false
		} else if !space {
			b.WriteRune(' ')
			space = true
		}
	}
	rs := []rune(" " + strings.TrimSpace(b.String()) + " ")
	g := map[string]float64{}
	for n := 2; n <= 3; n++ {
		for i := 0; i+n <= len(rs); i++ {
			g[string(rs[i:i+n])]++
		}
	}
	var sq float64
	for _, c := range g {
		sq += c * c
	}
	return vec{grams: g, norm: math.Sqrt(sq)}
}

func cosine(a, b vec) float64 {
	if a.norm == 0 || b.norm == 0 {
		return 0
	}
	if len(a.grams) > len(b.grams) {
		a, b = b, a
	}
	var dot float64
	for k, x := range a.grams {
		dot += x * b.grams[k]
	}
	return dot / (a.norm * b.norm)
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
