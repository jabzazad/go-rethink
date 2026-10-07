package decision

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
)

type goldenRow struct {
	Text    string  `json:"text"`
	Label   string  `json:"label"`
	Abstain bool    `json:"abstain"`
	Conf    float64 `json:"conf"`
}

type goldenFile struct {
	A, B float64
	Rows []goldenRow
}

func goldenInputs() []string {
	var in []string
	for _, e := range EvalSet {
		in = append(in, e.Text)
	}
	for _, e := range Corpus {
		in = append(in, e.Text)
	}
	in = append(in, "", "   ", "x", "hello world", "ขอบคุณครับ แล้วช่วยแนะนำหน่อยว่าแอร์ควรล้างกี่เดือน", "ล้านเปืดกี่โมงนะ", "อื่นๆ", "/ai สวัสดี")
	return in
}

// TestRethinkGolden pins Rethink's decisions so performance work can't change them.
// Regenerate with: UPDATE_GOLDEN=1 go test ./internal/decision -run TestRethinkGolden
func TestRethinkGolden(t *testing.T) {
	r := NewRethink(Corpus, 0.65)
	var got goldenFile
	got.A, got.B = r.A, r.B
	for _, text := range goldenInputs() {
		v := r.Decide(context.Background(), text)
		got.Rows = append(got.Rows, goldenRow{text, v.Label, v.Abstain, v.Confidence})
	}
	const path = "testdata/rethink_golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		b, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want goldenFile
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.A-want.A) > 1e-6 || math.Abs(got.B-want.B) > 1e-6 {
		t.Errorf("Platt params changed: got A=%.6f B=%.6f want A=%.6f B=%.6f", got.A, got.B, want.A, want.B)
	}
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("rows %d != %d", len(got.Rows), len(want.Rows))
	}
	for i, g := range got.Rows {
		w := want.Rows[i]
		if g.Label != w.Label || g.Abstain != w.Abstain || math.Abs(g.Conf-w.Conf) > 1e-6 {
			t.Errorf("%q: got %s/%v/%.6f want %s/%v/%.6f", g.Text, g.Label, g.Abstain, g.Conf, w.Label, w.Abstain, w.Conf)
		}
	}
}
