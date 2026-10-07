package embedding_test

import (
	"math"
	"testing"

	"mcp-foyer/internal/embedding"
)

// Similarity is about direction, so a service's vectors count whatever
// scale it produces them at.
func TestCosineOfTheSameDirectionIsOne(t *testing.T) {
	if got := embedding.Cosine([]float32{1, 2, 3}, []float32{2, 4, 6}); math.Abs(float64(got)-1) > 1e-6 {
		t.Errorf("Cosine = %v, want 1", got)
	}
}

func TestCosineOfIndependentVectorsIsZero(t *testing.T) {
	if got := embedding.Cosine([]float32{1, 0}, []float32{0, 1}); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("Cosine = %v, want 0", got)
	}
}

func TestCosineOfOppositesIsMinusOne(t *testing.T) {
	if got := embedding.Cosine([]float32{1, 0}, []float32{-1, 0}); math.Abs(float64(got)+1) > 1e-6 {
		t.Errorf("Cosine = %v, want -1", got)
	}
}

// Widths that do not match and vectors with no direction have nothing to
// compare; 0 is "nothing to say" rather than a number that would rank
// something for the wrong reason.
func TestCosineOfThingsWithNoDirectionIsZero(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
	}{
		{"differing widths", []float32{1, 2}, []float32{1}},
		{"the first empty", nil, []float32{1}},
		{"the second empty", []float32{1}, nil},
		{"both zero", []float32{0, 0}, []float32{0, 0}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embedding.Cosine(tc.a, tc.b); got != 0 {
				t.Errorf("Cosine = %v, want 0", got)
			}
		})
	}
}
