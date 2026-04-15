package index

import (
	"testing"
	"time"
)

func TestNewCrossEncoderRerankerDefaults(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{BaseURL: "http://localhost", Model: "test"})
	if r.topK != 20 {
		t.Fatalf("expected default topK=20, got %d", r.topK)
	}
	if r.scoreWeight != 0.5 {
		t.Fatalf("expected default scoreWeight=0.5, got %f", r.scoreWeight)
	}
	if r.maxRetries != 0 {
		t.Fatalf("expected default maxRetries=0, got %d", r.maxRetries)
	}
}

func TestNewCrossEncoderRerankerCustom(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		BaseURL:     "http://localhost:11434/",
		Model:       "llama3",
		TopK:        10,
		ScoreWeight: 0.7,
		Timeout:     10 * time.Second,
		MaxRetries:  3,
	})
	if r.baseURL != "http://localhost:11434" {
		t.Fatalf("expected trimmed baseURL, got %q", r.baseURL)
	}
	if r.model != "llama3" {
		t.Fatalf("expected model llama3, got %q", r.model)
	}
	if r.topK != 10 {
		t.Fatalf("expected topK=10, got %d", r.topK)
	}
	if r.scoreWeight != 0.7 {
		t.Fatalf("expected scoreWeight=0.7, got %f", r.scoreWeight)
	}
	if r.maxRetries != 3 {
		t.Fatalf("expected maxRetries=3, got %d", r.maxRetries)
	}
}

func TestNewCrossEncoderRerankerNegativeMaxRetries(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		BaseURL:    "http://localhost",
		Model:      "test",
		MaxRetries: -1,
	})
	if r.maxRetries != 2 {
		t.Fatalf("expected maxRetries=2 for negative input, got %d", r.maxRetries)
	}
}

func TestCrossEncoderRerankerName(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{BaseURL: "http://localhost", Model: "test"})
	if got := r.Name(); got != "cross_encoder" {
		t.Fatalf("expected Name() = %q, got %q", "cross_encoder", got)
	}
}

func TestParseScoreArrayValid(t *testing.T) {
	t.Parallel()
	scores, err := parseScoreArray("[0.1, 0.5, 0.9]", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	if scores[0] != 0.1 || scores[1] != 0.5 || scores[2] != 0.9 {
		t.Fatalf("unexpected scores: %v", scores)
	}
}

func TestParseScoreArrayWithSurroundingText(t *testing.T) {
	t.Parallel()
	scores, _ := parseScoreArray("Here are the scores: [0.2, 0.8] thanks!", 2)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	if scores[0] != 0.2 || scores[1] != 0.8 {
		t.Fatalf("unexpected scores: %v", scores)
	}
}

func TestParseScoreArrayInvalidJSON(t *testing.T) {
	t.Parallel()
	scores, err := parseScoreArray("[not valid json]", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []float32{0.5, 0.5, 0.5, 0.5}
	for i, s := range scores {
		if s != expected[i] {
			t.Fatalf("expected uniform fallback at index %d: got %f, want %f", i, s, expected[i])
		}
	}
}

func TestParseScoreArrayNoArray(t *testing.T) {
	t.Parallel()
	scores, _ := parseScoreArray("no array here", 3)
	for _, s := range scores {
		if s != 0.5 {
			t.Fatalf("expected uniform 0.5, got %f", s)
		}
	}
}

func TestParseScoreArrayEmptyContent(t *testing.T) {
	t.Parallel()
	scores, _ := parseScoreArray("", 2)
	for _, s := range scores {
		if s != 0.5 {
			t.Fatalf("expected uniform 0.5, got %f", s)
		}
	}
}

func TestParseScoreArrayMismatchedLength(t *testing.T) {
	t.Parallel()
	scores, _ := parseScoreArray("[0.8]", 3)
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	if scores[0] != 0.8 {
		t.Fatalf("expected first score 0.8, got %f", scores[0])
	}
	if scores[1] != 0.5 || scores[2] != 0.5 {
		t.Fatalf("expected fallback 0.5 for missing entries, got %f %f", scores[1], scores[2])
	}
}

func TestParseScoreArrayClamp(t *testing.T) {
	t.Parallel()
	scores, _ := parseScoreArray("[-0.5, 1.5]", 2)
	if scores[0] != 0 {
		t.Fatalf("expected clamped to 0, got %f", scores[0])
	}
	if scores[1] != 1 {
		t.Fatalf("expected clamped to 1, got %f", scores[1])
	}
}

func TestUniformScores(t *testing.T) {
	t.Parallel()
	scores := uniformScores(5, 0.7)
	if len(scores) != 5 {
		t.Fatalf("expected 5 scores, got %d", len(scores))
	}
	for _, s := range scores {
		if s != 0.7 {
			t.Fatalf("expected 0.7, got %f", s)
		}
	}
}
