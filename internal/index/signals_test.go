package index

import (
	"testing"
)

func TestFileTypeSignalName(t *testing.T) {
	t.Parallel()
	s := &FileTypeSignal{}
	if got := s.Name(); got != "file_type" {
		t.Fatalf("expected Name() = %q, got %q", "file_type", got)
	}
}

func TestFileTypeSignalWeight(t *testing.T) {
	t.Parallel()
	s := &FileTypeSignal{}
	if got := s.Weight(); got != 1.0 {
		t.Fatalf("expected Weight() = 1.0, got %f", got)
	}
}

func TestFileTypeSignalScore(t *testing.T) {
	t.Parallel()
	s := &FileTypeSignal{
		Extensions: map[string]float32{".go": 3, ".py": 2},
		Default:    0.5,
	}
	ctx := &SignalContext{}

	matchScore := s.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "pkg/handler.go"}})
	if matchScore <= 0 {
		t.Fatalf("expected positive score for matching extension, got %f", matchScore)
	}

	defaultScore := s.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "pkg/README"}})
	if defaultScore <= 0 {
		t.Fatalf("expected positive score for default extension, got %f", defaultScore)
	}

	if matchScore <= defaultScore {
		t.Fatalf("expected matching score (%f) > default score (%f)", matchScore, defaultScore)
	}

	extScore := s.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "app/util.py"}})
	if extScore <= 0 {
		t.Fatalf("expected positive score for .py extension, got %f", extScore)
	}
}

func TestRegisterSignal(t *testing.T) {
	registry := NewSignalRegistry()
	before := len(registry.List())
	registry.Register(&FileTypeSignal{Extensions: map[string]float32{".rs": 2}, Default: 0.5})
	after := registry.List()
	if len(after) != before+1 {
		t.Fatalf("expected %d signals after register, got %d", before+1, len(after))
	}
	if got := after[len(after)-1].Name(); got != "file_type" {
		t.Fatalf("expected last signal name %q, got %q", "file_type", got)
	}
}
