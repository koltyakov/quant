package index

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/koltyakov/quant/internal/llm"
	"github.com/koltyakov/quant/internal/logx"
)

type summaryJSON struct {
	Summary string   `json:"summary"`
	Topics  []string `json:"topics"`
}

type ChunkSummarizer struct {
	completer llm.Completer
	model     string
}

type SummarizerConfig struct {
	Completer llm.Completer
	Model     string
}

func NewChunkSummarizer(cfg SummarizerConfig) *ChunkSummarizer {
	return &ChunkSummarizer{
		completer: cfg.Completer,
		model:     cfg.Model,
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

	prompt := fmt.Sprintf(
		"Summarize this text in 1-2 concise sentences and extract up to 3 key topics. "+
			"Return JSON: {\"summary\": \"...\", \"topics\": [\"...\"]}\n\nText:\n%s", content,
	)

	resp, err := s.completer.Complete(ctx, llm.CompleteRequest{
		Model: s.model,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a text summarization system. Output only valid JSON."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, err
	}

	return parseSummaryResponse(resp.Content)
}

func (s *ChunkSummarizer) SummarizeBatch(ctx context.Context, contents []string) ([]*SummaryResult, error) {
	if len(contents) == 0 {
		return nil, nil
	}

	if len(contents) == 1 {
		result, err := s.Summarize(ctx, contents[0])
		if err != nil {
			return []*SummaryResult{{}}, err
		}
		return []*SummaryResult{result}, nil
	}

	return s.summarizeBatch(ctx, contents)
}

func (s *ChunkSummarizer) summarizeBatch(ctx context.Context, contents []string) ([]*SummaryResult, error) {
	results := make([]*SummaryResult, len(contents))

	batchSize := 4
	for batchStart := 0; batchStart < len(contents); batchStart += batchSize {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		batchEnd := min(batchStart+batchSize, len(contents))
		batch := contents[batchStart:batchEnd]

		batchResults, err := s.summarizeSubBatch(ctx, batch)
		if err != nil {
			logx.Warn("batch summarization failed; using empty summaries", "batch_start", batchStart, "err", err)
			for i := range batch {
				results[batchStart+i] = &SummaryResult{}
			}
			continue
		}
		for i, r := range batchResults {
			results[batchStart+i] = r
		}
	}

	return results, nil
}

func (s *ChunkSummarizer) summarizeSubBatch(ctx context.Context, contents []string) ([]*SummaryResult, error) {
	type textEntry struct {
		Text string `json:"text"`
	}

	entries := make([]textEntry, len(contents))
	for i, c := range contents {
		if len(c) > 2000 {
			c = c[:2000]
		}
		entries[i] = textEntry{Text: c}
	}

	entriesJSON, _ := json.Marshal(entries)
	prompt := fmt.Sprintf(
		"Summarize each text in 1-2 concise sentences and extract up to 3 key topics. "+
			"Return JSON array: [{\"summary\": \"...\", \"topics\": [\"...\"]}, ...]\n\nTexts:\n%s",
		string(entriesJSON),
	)

	resp, err := s.completer.Complete(ctx, llm.CompleteRequest{
		Model: s.model,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a text summarization system. Output only a valid JSON array."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, err
	}

	return parseBatchSummaryResponse(resp.Content, len(contents))
}

func parseBatchSummaryResponse(content string, expected int) ([]*SummaryResult, error) {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start < 0 || end < 0 || end <= start {
		return individualFallback(content, expected), nil
	}

	var parsed []summaryJSON
	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return individualFallback(content, expected), nil
	}

	results := make([]*SummaryResult, expected)
	for i := range results {
		if i < len(parsed) {
			results[i] = &SummaryResult{Summary: parsed[i].Summary, Topics: parsed[i].Topics}
		} else {
			results[i] = &SummaryResult{}
		}
	}
	return results, nil
}

func individualFallback(content string, expected int) []*SummaryResult {
	single, _ := parseSummaryResponse(content)
	results := make([]*SummaryResult, expected)
	for i := range results {
		if i == 0 && single != nil {
			results[i] = single
		} else {
			results[i] = &SummaryResult{}
		}
	}
	return results
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
