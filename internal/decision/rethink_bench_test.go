package decision

import (
	"context"
	"testing"
)

func BenchmarkRethinkDecide(b *testing.B) {
	r := NewRethink(Corpus, 0.65)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Decide(ctx, EvalSet[i%len(EvalSet)].Text)
	}
}

// Core scoring only, without building the Reason string.
func BenchmarkRethinkCore(b *testing.B) {
	r := NewRethink(Corpus, 0.65)
	sc := r.pool.Get().(*scratch)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q := r.vectorize(sc, EvalSet[i%len(EvalSet)].Text)
		r.predict(sc, q, -1)
	}
}
