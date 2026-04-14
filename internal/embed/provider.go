package embed

import (
	"context"
	"fmt"
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
	if strings.Contains(host, "openai.com") || strings.Contains(host, "api.openai.com") {
		return ProviderOpenAI
	}
	return ProviderOllama
}

func NewEmbedder(ctx context.Context, provider ProviderType, baseURL, model string) (Embedder, error) {
	switch provider {
	case ProviderOpenAI:
		return NewOpenAICompatible(ctx, baseURL, model)
	case ProviderOllama, ProviderUnknown:
		return NewOllama(ctx, baseURL, model)
	default:
		return nil, fmt.Errorf("unsupported embed provider: %s", provider)
	}
}
