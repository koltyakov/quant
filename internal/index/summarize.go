package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/logx"
)

type summaryJSON struct {
	Summary string   `json:"summary"`
	Topics  []string `json:"topics"`
}

type ChunkSummarizer struct {
	baseURL    string
	model      string
	httpClient *http.Client
	maxRetries int
}

type SummarizerConfig struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	MaxRetries int
}

func NewChunkSummarizer(cfg SummarizerConfig) *ChunkSummarizer {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 1
	}
	return &ChunkSummarizer{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		model:      cfg.Model,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		maxRetries: cfg.MaxRetries,
	}
}

type SummaryResult struct {
	Summary string
	Topics  []string
}

func (s *ChunkSummarizer) Summarize(ctx context.Context, content string) (*SummaryResult, error) {
	if len(content) > 2000 {
		content = content[:2000]
	}

	type chatReq struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
		Format   string        `json:"format,omitempty"`
	}

	prompt := fmt.Sprintf(
		"Summarize this text in 1-2 concise sentences and extract up to 3 key topics. "+
			"Return JSON: {\"summary\": \"...\", \"topics\": [\"...\"]}\n\nText:\n%s", content,
	)

	reqBody := chatReq{
		Model: s.model,
		Messages: []chatMessage{
			{Role: "system", Content: "You are a text summarization system. Output only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling summary request: %w", err)
	}

	var resp ollamaChatResponse
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		resp, err = s.doRequest(ctx, body)
		if err == nil {
			break
		}
		if attempt < s.maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(300*(attempt+1)) * time.Millisecond):
			}
		}
	}
	if err != nil {
		return nil, err
	}

	return parseSummaryResponse(resp.Message.Content)
}

func (s *ChunkSummarizer) SummarizeBatch(ctx context.Context, contents []string) ([]*SummaryResult, error) {
	results := make([]*SummaryResult, len(contents))
	for i, content := range contents {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result, err := s.Summarize(ctx, content)
		if err != nil {
			logx.Warn("chunk summarization failed", "index", i, "err", err)
			results[i] = &SummaryResult{Summary: "", Topics: nil}
			continue
		}
		results[i] = result
	}
	return results, nil
}

func (s *ChunkSummarizer) doRequest(ctx context.Context, body []byte) (ollamaChatResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return ollamaChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ollamaChatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ollamaChatResponse{}, fmt.Errorf("summarizer chat %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		return ollamaChatResponse{}, fmt.Errorf("summarizer permanent error %d", resp.StatusCode)
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ollamaChatResponse{}, fmt.Errorf("decoding summary response: %w", err)
	}
	return chatResp, nil
}

func parseSummaryResponse(content string) (*SummaryResult, error) {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < 0 || end <= start {
		return &SummaryResult{Summary: content[:min(len(content), 200)], Topics: nil}, nil
	}

	var parsed summaryJSON
	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return &SummaryResult{Summary: content[:min(len(content), 200)], Topics: nil}, nil
	}
	return &SummaryResult{Summary: parsed.Summary, Topics: parsed.Topics}, nil
}
