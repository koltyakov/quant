package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTruncateForEmbeddingPrefersBoundaries(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 40) + "\n\n" + strings.Repeat("delta epsilon zeta ", 40)
	truncated := truncateForEmbedding(text, 120)

	if len([]rune(truncated)) > 120 {
		t.Fatalf("expected truncated text to fit limit, got %d", len([]rune(truncated)))
	}
	if strings.HasSuffix(truncated, "alp") {
		t.Fatalf("expected truncation to avoid mid-token split, got %q", truncated)
	}
	if strings.TrimSpace(truncated) == "" {
		t.Fatal("expected non-empty truncated text")
	}
}

func TestOllamaEmbedBatchValidatesResponseCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{1, 0, 0}},
		})
	}))
	defer server.Close()

	o := &Ollama{
		baseURL:    server.URL,
		model:      "test-model",
		httpClient: server.Client(),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"one", "two"})
	if err == nil || !strings.Contains(err.Error(), "returned 1 embeddings for 2 inputs") {
		t.Fatalf("expected count validation error, got %v", err)
	}
}

func TestOllamaEmbedBatchValidatesDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{1, 0}},
		})
	}))
	defer server.Close()

	o := &Ollama{
		baseURL:    server.URL,
		model:      "test-model",
		dims:       3,
		httpClient: server.Client(),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"one"})
	if err == nil || !strings.Contains(err.Error(), "expected 3") {
		t.Fatalf("expected dimension validation error, got %v", err)
	}
}
