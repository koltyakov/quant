package embed

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type ProviderType string

const (
	ProviderOllama  ProviderType = "ollama"
	ProviderOpenAI  ProviderType = "openai"
	ProviderUnknown ProviderType = ""
)

func DetectProvider(embedURL string) ProviderType {
	host := strings.ToLower(embedURL)
	if strings.Contains(host, "openai.com") {
		return ProviderOpenAI
	}
	return ProviderOllama
}

func NewEmbedder(ctx context.Context, provider ProviderType, baseURL, model, apiKey string) (Embedder, error) {
	if provider == ProviderUnknown {
		p, err := inferProvider(baseURL)
		if err != nil {
			return nil, fmt.Errorf("embed_provider not set: %w (set --embed-provider to \"ollama\" or \"openai\")", err)
		}
		provider = p
	}
	switch provider {
	case ProviderOpenAI:
		return NewOpenAICompatible(ctx, baseURL, model, apiKey)
	case ProviderOllama:
		return NewOllama(ctx, baseURL, model)
	default:
		return nil, fmt.Errorf("unsupported embed provider: %s", provider)
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
