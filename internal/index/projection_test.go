package index

import (
	"bytes"
	"testing"
)

func TestNewRandomProjection_Dimensions(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(64, 16)
	if p.InDims() != 64 {
		t.Fatalf("expected inDims=64, got %d", p.InDims())
	}
	if p.OutDims() != 16 {
		t.Fatalf("expected outDims=16, got %d", p.OutDims())
	}
}

func TestNewRandomProjection_NonZeroWeights(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(32, 8)
	allZero := true
	for _, w := range p.weight {
		if w != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("expected at least one non-zero weight")
	}
}

func TestProjectionLayer_Project_CorrectDims(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(16, 4)
	input := make([]float32, 16)
	for i := range input {
		input[i] = 1.0
	}
	output := p.Project(input)
	if output == nil {
		t.Fatal("expected non-nil output for correct input dims")
	}
	if len(output) != 4 {
		t.Fatalf("expected output length 4, got %d", len(output))
	}
}

func TestProjectionLayer_Project_WrongDims(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(16, 4)
	output := p.Project([]float32{1.0, 2.0, 3.0})
	if output != nil {
		t.Fatalf("expected nil for wrong input dims, got %v", output)
	}
}

func TestProjectionLayer_InDims_OutDims(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(128, 32)
	if p.InDims() != 128 {
		t.Fatalf("expected InDims=128, got %d", p.InDims())
	}
	if p.OutDims() != 32 {
		t.Fatalf("expected OutDims=32, got %d", p.OutDims())
	}
}

func TestProjectionLayer_Encode_LoadProjection_RoundTrip(t *testing.T) {
	t.Parallel()
	orig := NewRandomProjection(16, 4)
	data := orig.Encode()
	loaded, err := LoadProjection(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.InDims() != orig.InDims() {
		t.Fatalf("expected inDims %d, got %d", orig.InDims(), loaded.InDims())
	}
	if loaded.OutDims() != orig.OutDims() {
		t.Fatalf("expected outDims %d, got %d", orig.OutDims(), loaded.OutDims())
	}
	if len(loaded.weight) != len(orig.weight) {
		t.Fatalf("expected %d weights, got %d", len(orig.weight), len(loaded.weight))
	}
	for i := range orig.weight {
		if loaded.weight[i] != orig.weight[i] {
			t.Fatalf("weight mismatch at index %d: expected %f, got %f", i, orig.weight[i], loaded.weight[i])
		}
	}
	for i := range orig.bias {
		if loaded.bias[i] != orig.bias[i] {
			t.Fatalf("bias mismatch at index %d: expected %f, got %f", i, orig.bias[i], loaded.bias[i])
		}
	}
	input := make([]float32, 16)
	for i := range input {
		input[i] = float32(i) * 0.1
	}
	origOut := orig.Project(input)
	loadedOut := loaded.Project(input)
	if len(origOut) != len(loadedOut) {
		t.Fatalf("expected output length %d, got %d", len(origOut), len(loadedOut))
	}
	for i := range origOut {
		if origOut[i] != loadedOut[i] {
			t.Fatalf("projected output mismatch at %d: %f != %f", i, origOut[i], loadedOut[i])
		}
	}
}

func TestLoadProjection_DataTooSmall(t *testing.T) {
	t.Parallel()
	_, err := LoadProjection([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("expected error for data too small")
	}
}

func TestLoadProjection_TruncatedData(t *testing.T) {
	t.Parallel()
	orig := NewRandomProjection(8, 4)
	data := orig.Encode()
	_, err := LoadProjection(data[:12])
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestProjectionLayer_Encode_Deterministic(t *testing.T) {
	t.Parallel()
	p := NewRandomProjection(4, 2)
	enc1 := p.Encode()
	enc2 := p.Encode()
	if !bytes.Equal(enc1, enc2) {
		t.Fatal("expected deterministic encoding from same layer")
	}
}
