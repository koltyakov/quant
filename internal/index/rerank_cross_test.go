package index

import (
	"context"
	"reflect"
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
	for _, response := range []string{"[not valid json]", "no array", "", "[]", "[0.8]", "[0.1,0.2,0.3]", "[null,0.5]", "[NaN,0.5]", "[1e400,0.5]", "[1e100,0.5]"} {
		t.Run(response, func(t *testing.T) {
			r := NewCrossEncoderReranker(CrossEncoderConfig{Completer: &stubCompleter{response: response}})
			original := []SearchResult{{ChunkID: 1, Score: .9, ScoreKind: "rrf"}, {ChunkID: 2, Score: .8, ScoreKind: "rrf"}}
			before := append([]SearchResult(nil), original...)
			out, err := r.Rerank(context.Background(), "q", nil, original)
			if err == nil || !reflect.DeepEqual(out, before) || !reflect.DeepEqual(original, before) {
				t.Fatalf("invalid response must error without changing results: out=%v err=%v", out, err)
			}
		})
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

func TestCrossEncoderScoredPoolOnly(t *testing.T) {
	r := NewCrossEncoderReranker(CrossEncoderConfig{TopK: 2, Completer: &stubCompleter{response: "[0.2,0.9]"}})
	out, err := r.Rerank(context.Background(), "q", nil, []SearchResult{{ChunkID: 1, Score: .9}, {ChunkID: 2, Score: .8}, {ChunkID: 3, Score: .7}})
	if err != nil || len(out) != 2 {
		t.Fatalf("out=%v err=%v", out, err)
	}
	for _, result := range out {
		if result.ScoreKind != "rerank_blend" || result.ChunkID == 3 {
			t.Fatalf("unscored or mislabeled result: %+v", result)
		}
	}
}

func TestCrossEncoderLateBatchFailurePreservesAllResults(t *testing.T) {
	// The first batch succeeds; the same eight scores are wrong for batch two.
	r := NewCrossEncoderReranker(CrossEncoderConfig{Completer: &stubCompleter{response: "[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8]"}})
	in := make([]SearchResult, 9)
	for i := range in {
		in[i] = SearchResult{ChunkID: int64(i + 1), Score: float32(9-i) / 10, ScoreKind: "rrf"}
	}
	before := append([]SearchResult(nil), in...)
	out, err := r.Rerank(context.Background(), "q", nil, in)
	if err == nil || !reflect.DeepEqual(out, before) || !reflect.DeepEqual(in, before) {
		t.Fatalf("out=%v err=%v", out, err)
	}
}
