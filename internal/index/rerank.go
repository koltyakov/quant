package index

import (
	"context"
	"sort"
)

type Reranker interface {
	Rerank(ctx context.Context, query string, queryEmbedding []float32, results []SearchResult) ([]SearchResult, error)
	Name() string
}

type NoopReranker struct{}

func (r *NoopReranker) Rerank(_ context.Context, _ string, _ []float32, results []SearchResult) ([]SearchResult, error) {
	return results, nil
}

func (r *NoopReranker) Name() string { return "noop" }

type EmbeddingReranker struct {
	scoreWeight float32
	topK        int
}

func NewEmbeddingReranker(topK int, scoreWeight float32) *EmbeddingReranker {
	if topK < 1 {
		topK = 20
	}
	if scoreWeight <= 0 {
		scoreWeight = 0.3
	}
	return &EmbeddingReranker{scoreWeight: scoreWeight, topK: topK}
}

func (r *EmbeddingReranker) Name() string { return "embedding_rerank" }

func (r *EmbeddingReranker) Rerank(_ context.Context, _ string, queryEmbedding []float32, results []SearchResult) ([]SearchResult, error) {
	if len(results) <= 1 || len(queryEmbedding) == 0 {
		return results, nil
	}

	candidates := results
	if len(candidates) > r.topK {
		candidates = candidates[:r.topK]
	}

	maxOriginal := float32(0)
	for _, res := range candidates {
		if res.Score > maxOriginal {
			maxOriginal = res.Score
		}
	}
	if maxOriginal == 0 {
		maxOriginal = 1
	}

	type scored struct {
		result   SearchResult
		combined float32
	}

	scored_results := make([]scored, len(candidates))
	for i, res := range candidates {
		normalizedOriginal := res.Score / maxOriginal
		scored_results[i] = scored{
			result:   res,
			combined: (1-r.scoreWeight)*normalizedOriginal + r.scoreWeight*normalizedOriginal,
		}
	}

	sort.SliceStable(scored_results, func(i, j int) bool {
		return scored_results[i].combined > scored_results[j].combined
	})

	out := make([]SearchResult, len(results))
	reranked := make([]SearchResult, len(scored_results))
	for i, s := range scored_results {
		s.result.Score = s.combined
		reranked[i] = s.result
	}

	copy(out, reranked)
	if len(results) > len(reranked) {
		copy(out[len(reranked):], results[len(reranked):])
	} else {
		out = reranked
	}

	return out, nil
}
