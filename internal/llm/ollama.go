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

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type OllamaCompleter struct {
	baseURL    string
	httpClient *http.Client
	maxRetries int
}

func NewOllamaCompleter(cfg Config) *OllamaCompleter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 2
	}
	return &OllamaCompleter{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		httpClient: &http.Client{Timeout: cfg.Timeout},
		maxRetries: cfg.MaxRetries,
	}
}

func (o *OllamaCompleter) Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return CompleteResponse{}, fmt.Errorf("marshaling chat request: %w", err)
	}

	var resp ollamaResponse
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

	return CompleteResponse{Content: resp.Message.Content}, nil
}

func (o *OllamaCompleter) doRequest(ctx context.Context, body []byte) (ollamaResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return ollamaResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return ollamaResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ollamaResponse{}, fmt.Errorf("ollama chat %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		return ollamaResponse{}, fmt.Errorf("ollama chat permanent error %d", resp.StatusCode)
	}

	var chatResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ollamaResponse{}, fmt.Errorf("decoding chat response: %w", err)
	}
	return chatResp, nil
}
