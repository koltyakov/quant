package index

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/koltyakov/quant/internal/llm"
	"github.com/koltyakov/quant/internal/logx"
)

type rerankPair struct {
	index    int
	document string
}

type CrossEncoderReranker struct {
	completer   llm.Completer
	model       string
	topK        int
	scoreWeight float32
}

type CrossEncoderConfig struct {
	Completer   llm.Completer
	Model       string
	TopK        int
	ScoreWeight float32
}

func NewCrossEncoderReranker(cfg CrossEncoderConfig) *CrossEncoderReranker {
	if cfg.TopK < 1 {
		cfg.TopK = 20
	}
	if cfg.ScoreWeight <= 0 || cfg.ScoreWeight > 1 || math.IsNaN(float64(cfg.ScoreWeight)) {
		cfg.ScoreWeight = 0.5
	}
	return &CrossEncoderReranker{
		completer:   cfg.Completer,
		model:       cfg.Model,
		topK:        cfg.TopK,
		scoreWeight: cfg.ScoreWeight,
	}
}

func (r *CrossEncoderReranker) Name() string { return "cross_encoder" }

func (r *CrossEncoderReranker) Rerank(ctx context.Context, query string, _ []float32, results []SearchResult) ([]SearchResult, error) {
	if len(results) <= 1 {
		return results, nil
	}

	candidates := results
	if len(candidates) > r.topK {
		candidates = candidates[:r.topK]
	}

	scores, err := r.scoreBatch(ctx, query, candidates)
	if err != nil {
		logx.Warn("cross-encoder rerank failed; keeping original order", "err", err)
		return results, err
	}

	maxOriginal := float32(0)
	for _, res := range candidates {
		if math.IsNaN(float64(res.Score)) || math.IsInf(float64(res.Score), 0) {
			return results, fmt.Errorf("nonfinite original score for chunk %d", res.ChunkID)
		}
		if res.Score > maxOriginal {
			maxOriginal = res.Score
		}
	}
	if maxOriginal == 0 {
		maxOriginal = 1
	}

	maxRerank := float32(0)
	for _, s := range scores {
		if s > maxRerank {
			maxRerank = s
		}
	}
	if maxRerank == 0 {
		maxRerank = 1
	}

	type scored struct {
		result   SearchResult
		combined float32
	}
	scoredResults := make([]scored, len(candidates))
	for i, res := range candidates {
		normalizedOriginal := res.Score / maxOriginal
		normalizedRerank := scores[i] / maxRerank
		scoredResults[i] = scored{
			result:   res,
			combined: (1-r.scoreWeight)*normalizedOriginal + r.scoreWeight*normalizedRerank,
		}
	}

	sort.SliceStable(scoredResults, func(i, j int) bool {
		return scoredResults[i].combined > scoredResults[j].combined
	})

	// Return only the scored pool: its normalized blend is not comparable to
	// the original RRF scores of the unscored tail.
	out := make([]SearchResult, len(scoredResults))
	for i, s := range scoredResults {
		s.result.Score = s.combined
		s.result.ScoreKind = "rerank_blend"
		out[i] = s.result
	}

	return out, nil
}

func (r *CrossEncoderReranker) scoreBatch(ctx context.Context, query string, candidates []SearchResult) ([]float32, error) {
	scores := make([]float32, len(candidates))
	pairs := make([]rerankPair, len(candidates))
	for i, c := range candidates {
		pairs[i] = rerankPair{index: i, document: c.ChunkContent}
	}

	batchSize := 8
	for batchStart := 0; batchStart < len(pairs); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(pairs))
		batch := pairs[batchStart:batchEnd]

		batchScores, err := r.scoreSubBatch(ctx, query, batch)
		if err != nil {
			return nil, err
		}
		for i, s := range batchScores {
			scores[batch[i].index] = s
		}
	}

	return scores, nil
}

func (r *CrossEncoderReranker) scoreSubBatch(ctx context.Context, query string, pairs []rerankPair) ([]float32, error) {
	type docEntry struct {
		Document string `json:"document"`
	}

	docs := make([]docEntry, len(pairs))
	for i, p := range pairs {
		docs[i] = docEntry{Document: truncateUTF8(p.document, 800)}
	}

	docsJSON, _ := json.Marshal(docs)
	prompt := fmt.Sprintf(
		"Score each document's relevance to the query on a scale of 0.0 to 1.0. "+
			"Return ONLY a JSON array of numbers, one per document, in order.\n\n"+
			"Query: %s\n\nDocuments:\n%s",
		query, string(docsJSON),
	)

	resp, err := r.completer.Complete(ctx, llm.CompleteRequest{
		Model: r.model,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a relevance scoring system. Output only a JSON array of float scores between 0.0 and 1.0."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, err
	}

	return parseScoreArray(resp.Content, len(pairs))
}

func parseScoreArray(content string, expected int) ([]float32, error) {
	content = strings.TrimSpace(content)

	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("reranker response has no score array")
	}

	var raw []*float64
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("invalid reranker scores: %w", err)
	}
	if len(raw) != expected {
		return nil, fmt.Errorf("reranker returned %d scores, expected %d", len(raw), expected)
	}

	scores := make([]float32, expected)
	for i := range scores {
		if raw[i] == nil || math.IsNaN(*raw[i]) || math.IsInf(*raw[i], 0) {
			return nil, fmt.Errorf("reranker score %d is missing or nonfinite", i)
		}
		s := float32(*raw[i])
		if math.IsInf(float64(s), 0) {
			return nil, fmt.Errorf("reranker score %d overflows float32", i)
		}
		if s < 0 {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		scores[i] = s
	}
	return scores, nil
}
