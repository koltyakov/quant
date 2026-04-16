package llm

import (
	"context"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompleteRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   string    `json:"format,omitempty"`
}

type CompleteResponse struct {
	Content string
}

type Completer interface {
	Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error)
}

type Config struct {
	BaseURL    string
	Model      string
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
}

func NewCompleter(cfg Config) Completer {
	if cfg.APIKey != "" || isOpenAIURL(cfg.BaseURL) {
		return NewOpenAICompleter(cfg)
	}
	return NewOllamaCompleter(cfg)
}

func isOpenAIURL(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "openai.com")
}
