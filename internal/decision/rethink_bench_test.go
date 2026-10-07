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
