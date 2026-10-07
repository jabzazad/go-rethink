package decision

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRethinkReasonFormat(t *testing.T) {
	r := NewRethink(Corpus, 0.65)
	for _, text := range []string{"เปิดกี่โมงครับ", "good morning", "แอร์เปิดแล้วมีกลิ่นอับ ล้างเองได้ไหม", ""} {
		v := r.Decide(context.Background(), text)
		t.Logf("%-40q -> %s", text, v.Reason)
		if v.Reason == "" || strings.Contains(v.Reason, "%!") {
			t.Errorf("bad reason for %q: %q", text, v.Reason)
		}
	}
}

func TestAppendHelpers(t *testing.T) {
	for _, x := range []float64{0, 0.004, 0.005, 0.18, 0.5, 0.65, 0.999, 1, 1.0} {
		if got, want := string(appendF(nil, x)), fmt.Sprintf("%.2f", x); got != want {
			t.Errorf("appendF(%v) = %s, want %s", x, got, want)
		}
	}
	for _, s := range []string{"price", `a"b`, `a\b`, "ไทย"} {
		if got, want := string(appendQuoted(nil, s)), fmt.Sprintf("%q", s); got != want {
			t.Errorf("appendQuoted(%q) = %s, want %s", s, got, want)
		}
	}
}
