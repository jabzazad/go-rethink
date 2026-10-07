package decision

import "testing"

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
