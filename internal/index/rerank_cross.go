package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/logx"
)

type rerankPair struct {
	index    int
	document string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type CrossEncoderReranker struct {
	baseURL     string
	model       string
	topK        int
	scoreWeight float32
	httpClient  *http.Client
	maxRetries  int
}

type CrossEncoderConfig struct {
	BaseURL     string
	Model       string
	TopK        int
	ScoreWeight float32
	Timeout     time.Duration
	MaxRetries  int
}

func NewCrossEncoderReranker(cfg CrossEncoderConfig) *CrossEncoderReranker {
	if cfg.TopK < 1 {
		cfg.TopK = 20
	}
	if cfg.ScoreWeight <= 0 {
		cfg.ScoreWeight = 0.5
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 2
	}
	return &CrossEncoderReranker{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		model:       cfg.Model,
		topK:        cfg.TopK,
		scoreWeight: cfg.ScoreWeight,
		httpClient:  &http.Client{Timeout: cfg.Timeout},
		maxRetries:  cfg.MaxRetries,
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
		return results, nil
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

	out := make([]SearchResult, len(results))
	for i, s := range scoredResults {
		s.result.Score = s.combined
		out[i] = s.result
	}
	if len(results) > len(scoredResults) {
		copy(out[len(scoredResults):], results[len(scoredResults):])
	} else {
		out = out[:len(scoredResults)]
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
		batchEnd := batchStart + batchSize
		if batchEnd > len(pairs) {
			batchEnd = len(pairs)
		}
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

	type chatReq struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
	}

	docs := make([]docEntry, len(pairs))
	for i, p := range pairs {
		content := p.document
		if len(content) > 800 {
			content = content[:800]
		}
		docs[i] = docEntry{Document: content}
	}

	docsJSON, _ := json.Marshal(docs)
	prompt := fmt.Sprintf(
		"Score each document's relevance to the query on a scale of 0.0 to 1.0. "+
			"Return ONLY a JSON array of numbers, one per document, in order.\n\n"+
			"Query: %s\n\nDocuments:\n%s",
		query, string(docsJSON),
	)

	reqBody := chatReq{
		Model: r.model,
		Messages: []chatMessage{
			{Role: "system", Content: "You are a relevance scoring system. Output only a JSON array of float scores between 0.0 and 1.0."},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling rerank request: %w", err)
	}

	var chatResp ollamaChatResponse
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		chatResp, err = r.doRequest(ctx, body)
		if err == nil {
			break
		}
		if attempt < r.maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
			}
		}
	}
	if err != nil {
		return nil, err
	}

	return parseScoreArray(chatResp.Message.Content, len(pairs))
}

func (r *CrossEncoderReranker) doRequest(ctx context.Context, body []byte) (ollamaChatResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return ollamaChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return ollamaChatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ollamaChatResponse{}, fmt.Errorf("ollama chat %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		return ollamaChatResponse{}, fmt.Errorf("ollama chat permanent error %d", resp.StatusCode)
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ollamaChatResponse{}, fmt.Errorf("decoding chat response: %w", err)
	}
	return chatResp, nil
}

func parseScoreArray(content string, expected int) ([]float32, error) {
	content = strings.TrimSpace(content)

	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start < 0 || end < 0 || end <= start {
		return uniformScores(expected, 0.5), nil
	}

	var raw []float64
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return uniformScores(expected, 0.5), nil
	}

	scores := make([]float32, expected)
	for i := range scores {
		s := float32(0.5)
		if i < len(raw) {
			s = float32(raw[i])
			if s < 0 {
				s = 0
			}
			if s > 1 {
				s = 1
			}
		}
		scores[i] = s
	}
	return scores, nil
}

func uniformScores(n int, v float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = v
	}
	return s
}
