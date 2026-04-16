package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	chatURL    string
	apiKey     string
	httpClient *http.Client
	maxRetries int
}

func NewOpenAICompleter(cfg Config) (*OpenAICompleter, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 2
	}
	return newOpenAICompleter(cfg, &http.Client{Timeout: cfg.Timeout})
}

func newOpenAICompleter(cfg Config, httpClient *http.Client) (*OpenAICompleter, error) {
	chatURL, err := openAIChatURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	return &OpenAICompleter{
		chatURL:    chatURL,
		apiKey:     cfg.APIKey,
		httpClient: httpClient,
		maxRetries: cfg.MaxRetries,
	}, nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.chatURL, bytes.NewReader(body))
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

func openAIChatURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("llm URL must be a valid URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("llm URL must be an absolute URL with scheme and host")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("llm URL scheme must be http or https")
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case path == "", path == "/v1":
		path = "/v1/chat/completions"
	case strings.HasSuffix(path, "/chat/completions"):
	case strings.HasSuffix(path, "/embeddings"):
		path = strings.TrimSuffix(path, "/embeddings") + "/chat/completions"
	case strings.HasSuffix(path, "/embed"):
		path = strings.TrimSuffix(path, "/embed") + "/chat/completions"
	default:
		path += "/v1/chat/completions"
	}

	chatURL := *parsed
	chatURL.Path = path
	chatURL.RawPath = ""
	chatURL.Fragment = ""
	return chatURL.String(), nil
}
