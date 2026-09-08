package index

import (
	"context"
	"path/filepath"
	"testing"
)

func TestThresholdBeforeDiversity(t *testing.T) {
	in := []SearchResult{
		{ChunkID: 1, DocumentPath: "A", ChunkIndex: 0, Score: .9},
		{ChunkID: 2, DocumentPath: "A", ChunkIndex: 1, Score: .8},
		{ChunkID: 3, DocumentPath: "B", ChunkIndex: 0, Score: .2},
	}
	out := finalizeSearchResults(in, .5, 2)
	if len(out) != 2 || out[0].ChunkID != 1 || out[1].ChunkID != 2 {
		t.Fatalf("eligible A chunks lost: %+v", out)
	}
}

func TestSemanticCandidateBudgetIndependentOfOutput(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatal(err)
	}
	mustCloseStore(t, s)
	seedSimilarChunks(t, s)
	s.SetReranker(NewCrossEncoderReranker(CrossEncoderConfig{ScoreWeight: 1, Completer: &stubCompleter{response: "[0.1,0.2,0.9]"}}))
	for _, graph := range []bool{false, true} {
		if graph {
			if err := s.BuildHNSW(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		ctx, diag := WithRetrievalDiagnostics(context.Background())
		out, err := s.Search(ctx, "", []float32{1, 0}, 1, "sim/")
		if err != nil || len(out) != 1 || diag.Semantic != "complete" {
			t.Fatalf("out=%v diag=%+v err=%v", out, diag, err)
		}
		if out[0].ScoreKind != "rerank_blend" || out[0].DocumentPath != "sim/far.txt" {
			t.Fatalf("limit=1 discarded semantic candidates before rerank: %+v", out)
		}
		candidates, err := s.collectVectorCandidates(ctx, []float32{1, 0}, 100, "sim/", nil, nil, "", nil)
		if err != nil || len(candidates) != 3 {
			t.Fatalf("semantic pool=%d err=%v", len(candidates), err)
		}
	}
}
