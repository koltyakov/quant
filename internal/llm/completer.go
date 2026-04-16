package llm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type ProviderType string

const (
	ProviderOllama  ProviderType = "ollama"
	ProviderOpenAI  ProviderType = "openai"
	ProviderUnknown ProviderType = ""
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
	Provider   ProviderType
	BaseURL    string
	Model      string
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
}

func NewCompleter(cfg Config) (Completer, error) {
	provider := cfg.Provider
	if provider == ProviderUnknown {
		inferred, err := inferProvider(cfg.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("llm_provider not set: %w (set --llm-provider to \"ollama\" or \"openai\")", err)
		}
		provider = inferred
	}
	switch provider {
	case ProviderOpenAI:
		return NewOpenAICompleter(cfg)
	case ProviderOllama:
		return NewOllamaCompleter(cfg)
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", provider)
	}
}

func inferProvider(raw string) (ProviderType, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ProviderUnknown, fmt.Errorf("cannot parse URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return ProviderOllama, nil
	}
	if strings.Contains(host, "openai.com") {
		return ProviderOpenAI, nil
	}
	return ProviderUnknown, fmt.Errorf("cannot determine provider from URL %q", raw)
}
