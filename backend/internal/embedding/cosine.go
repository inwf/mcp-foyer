package embedding

import "math"

// Cosine is the cosine similarity of two vectors: 1 when they point the
// same way, 0 when they are unrelated, -1 when they are opposites. It is
// what ranks a query against the tools it might mean.
//
// Differing widths and zero vectors return 0 — "nothing to say" — rather
// than a number that would rank something for the wrong reason.
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i, x := range a {
		y := b[i]
		dot += float64(x) * float64(y)
		normA += float64(x) * float64(x)
		normB += float64(y) * float64(y)
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}
