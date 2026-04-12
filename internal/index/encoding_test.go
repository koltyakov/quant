package index

import (
	"container/heap"
	"math"
	"testing"
)

func TestEncodeInt8_Empty(t *testing.T) {
	result := EncodeInt8(nil)
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestEncodeInt8_Basic(t *testing.T) {
	vec := []float32{0.0, 0.5, 1.0}
	encoded := EncodeInt8(vec)
	if len(encoded) != 8+len(vec) {
		t.Fatalf("expected %d bytes, got %d", 8+len(vec), len(encoded))
	}
}

func TestEncodeInt8_RoundTrip(t *testing.T) {
	vec := []float32{-1.0, 0.0, 0.5, 1.0, -0.5}
	encoded := EncodeInt8(vec)

	minVal := math.Float32frombits(decodeUint32(encoded[0:4]))
	scale := math.Float32frombits(decodeUint32(encoded[4:8]))

	if scale < 0 {
		t.Fatalf("scale should not be negative, got %f", scale)
	}

	for i, v := range vec {
		var expected float32
		if scale > 0 {
			expected = float32(encoded[8+i])*scale + minVal
		}
		delta := float32(math.Abs(float64(v - expected)))
		tolerance := float32(0.02)
		if delta > tolerance {
			t.Errorf("element %d: expected ~%f, got %f (delta=%f)", i, v, expected, delta)
		}
	}
}

func TestEncodeInt8_ConstantVector(t *testing.T) {
	vec := []float32{0.5, 0.5, 0.5}
	encoded := EncodeInt8(vec)
	if len(encoded) != 8+len(vec) {
		t.Fatalf("expected %d bytes, got %d", 8+len(vec), len(encoded))
	}
	for i := 0; i < len(vec); i++ {
		if encoded[8+i] != 0 {
			t.Errorf("expected 0 for constant vector quantized value, got %d", encoded[8+i])
		}
	}
}

func decodeUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func TestDotProductEncoded_Int8Format(t *testing.T) {
	vec := []float32{0.2, 0.8}
	encoded := EncodeInt8(vec)
	score := dotProductEncoded(vec, encoded)
	if math.IsNaN(float64(score)) || math.IsInf(float64(score), 0) {
		t.Fatalf("invalid dot product score: %f", score)
	}
}

func TestDotProductEncoded_UnknownFormat(t *testing.T) {
	vec := []float32{1.0, 2.0}
	score := dotProductEncoded(vec, []byte{0x00})
	if score != 0 {
		t.Errorf("expected 0 for unknown format, got %f", score)
	}
}

func TestDotProduct_MismatchedLengths(t *testing.T) {
	score := dotProduct([]float32{1.0}, []float32{1.0, 2.0})
	if score != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %f", score)
	}
}

func TestCandidateHeap(t *testing.T) {
	h := &candidateHeap{}
	heap.Init(h)

	items := []scoredResult{
		{score: 0.5, path: "b"},
		{score: 0.1, path: "a"},
		{score: 0.9, path: "c"},
	}
	for _, item := range items {
		heap.Push(h, item)
	}

	if h.Len() != 3 {
		t.Fatalf("expected 3 items, got %d", h.Len())
	}

	min := heap.Pop(h).(scoredResult)
	if min.score != 0.1 {
		t.Fatalf("expected min score 0.1, got %f", min.score)
	}
	if min.path != "a" {
		t.Fatalf("expected path 'a', got %q", min.path)
	}

	min2 := heap.Pop(h).(scoredResult)
	if min2.score != 0.5 {
		t.Fatalf("expected score 0.5, got %f", min2.score)
	}

	min3 := heap.Pop(h).(scoredResult)
	if min3.score != 0.9 {
		t.Fatalf("expected score 0.9, got %f", min3.score)
	}
}

func TestApplyWeightOverrides_NoOverride(t *testing.T) {
	w := applyWeightOverrides(QuerySignalWeights{Keyword: 1.0, Vector: 1.0}, 0, 0)
	if w.Keyword != 1.0 || w.Vector != 1.0 {
		t.Fatalf("expected unchanged weights, got k=%f v=%f", w.Keyword, w.Vector)
	}
}

func TestApplyWeightOverrides_KeywordOverride(t *testing.T) {
	w := applyWeightOverrides(QuerySignalWeights{Keyword: 1.0, Vector: 1.0}, 2.0, 0)
	if w.Keyword != 2.0 {
		t.Fatalf("expected keyword=2.0, got %f", w.Keyword)
	}
	if w.Vector <= 0 {
		t.Fatalf("expected vector > 0, got %f", w.Vector)
	}
}

func TestApplyWeightOverrides_VectorOverride(t *testing.T) {
	w := applyWeightOverrides(QuerySignalWeights{Keyword: 1.0, Vector: 1.0}, 0, 3.0)
	if w.Vector != 3.0 {
		t.Fatalf("expected vector=3.0, got %f", w.Vector)
	}
	if w.Keyword <= 0 {
		t.Fatalf("expected keyword > 0, got %f", w.Keyword)
	}
}

func TestApplyWeightOverrides_BothOverrides(t *testing.T) {
	w := applyWeightOverrides(QuerySignalWeights{Keyword: 1.0, Vector: 1.0}, 2.0, 3.0)
	if w.Keyword <= 0 || w.Vector <= 0 {
		t.Fatalf("expected positive weights, got k=%f v=%f", w.Keyword, w.Vector)
	}
}
