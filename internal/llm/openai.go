package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type OpenAICompleter struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	maxRetries int
}

func NewOpenAICompleter(cfg Config) *OpenAICompleter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 2
	}
	return &OpenAICompleter{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		maxRetries: cfg.MaxRetries,
	}
}

func (o *OpenAICompleter) Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return CompleteResponse{}, fmt.Errorf("marshaling chat request: %w", err)
	}

	var resp openAIChatResponse
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		resp, err = o.doRequest(ctx, body)
		if err == nil {
			break
		}
		if attempt < o.maxRetries {
			select {
			case <-ctx.Done():
				return CompleteResponse{}, ctx.Err()
			case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
			}
		}
	}
	if err != nil {
		return CompleteResponse{}, err
	}

	if len(resp.Choices) == 0 {
		return CompleteResponse{}, fmt.Errorf("openai chat returned no choices")
	}

	return CompleteResponse{Content: resp.Choices[0].Message.Content}, nil
}

func (o *OpenAICompleter) doRequest(ctx context.Context, body []byte) (openAIChatResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return openAIChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return openAIChatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return openAIChatResponse{}, fmt.Errorf("openai chat %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return openAIChatResponse{}, fmt.Errorf("openai chat permanent error %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return openAIChatResponse{}, fmt.Errorf("decoding chat response: %w", err)
	}
	return chatResp, nil
}
