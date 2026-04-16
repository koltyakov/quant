package index

import (
	"context"
	"testing"

	"github.com/koltyakov/quant/internal/llm"
)

type stubCompleter struct {
	response string
	err      error
}

func (s *stubCompleter) Complete(_ context.Context, _ llm.CompleteRequest) (llm.CompleteResponse, error) {
	return llm.CompleteResponse{Content: s.response}, s.err
}

func TestNewCrossEncoderRerankerDefaults(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		Completer: &stubCompleter{},
		Model:     "test",
	})
	if r.topK != 20 {
		t.Fatalf("expected default topK=20, got %d", r.topK)
	}
	if r.scoreWeight != 0.5 {
		t.Fatalf("expected default scoreWeight=0.5, got %f", r.scoreWeight)
	}
}

func TestNewCrossEncoderRerankerCustom(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		Completer:   &stubCompleter{},
		Model:       "llama3",
		TopK:        10,
		ScoreWeight: 0.7,
	})
	if r.model != "llama3" {
		t.Fatalf("expected model llama3, got %q", r.model)
	}
	if r.topK != 10 {
		t.Fatalf("expected topK=10, got %d", r.topK)
	}
	if r.scoreWeight != 0.7 {
		t.Fatalf("expected scoreWeight=0.7, got %f", r.scoreWeight)
	}
}

func TestCrossEncoderRerankerName(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		Completer: &stubCompleter{},
		Model:     "test",
	})
	if got := r.Name(); got != "cross_encoder" {
		t.Fatalf("expected Name() = %q, got %q", "cross_encoder", got)
	}
}

func TestCrossEncoderRerankerRerank(t *testing.T) {
	t.Parallel()
	completer := &stubCompleter{response: "[0.2, 0.9]"}
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		Completer: completer,
		Model:     "test",
		TopK:      10,
	})

	results := []SearchResult{
		{ChunkID: 1, Score: 0.8, ChunkContent: "first"},
		{ChunkID: 2, Score: 0.2, ChunkContent: "second"},
	}

	out, err := r.Rerank(context.Background(), "query", nil, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].ChunkID != 2 {
		t.Fatalf("expected chunk 2 ranked first after rerank, got chunk %d", out[0].ChunkID)
	}
}

func TestCrossEncoderRerankerSingleResult(t *testing.T) {
	t.Parallel()
	r := NewCrossEncoderReranker(CrossEncoderConfig{
		Completer: &stubCompleter{},
		Model:     "test",
	})
	results := []SearchResult{{ChunkID: 1, Score: 0.5}}
	out, err := r.Rerank(context.Background(), "q", nil, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ChunkID != 1 {
		t.Fatalf("single result should pass through unchanged")
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
