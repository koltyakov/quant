package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newTestHTTPClient(t *testing.T, statusCode int, body any) *http.Client {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal test response: %v", err)
	}

	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("expected POST request, got %s", req.Method)
			}
			if req.URL.String() != "http://ollama.test/api/embed" {
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}

			return &http.Response{
				StatusCode: statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(payload))),
				Request:    req,
			}, nil
		}),
	}
}

func TestTruncateForEmbeddingPrefersBoundaries(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 40) + "\n\n" + strings.Repeat("delta epsilon zeta ", 40)
	truncated := TruncateForInput(text, 120)

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

func TestPrefixWithinInputBudget_ReturnsConsumedRunes(t *testing.T) {
	text := "alpha beta gamma\n\ndelta epsilon"
	prefix, consumed := PrefixWithinInputBudget(text, 18)
	if prefix == "" {
		t.Fatal("expected non-empty prefix")
	}
	if consumed <= 0 || consumed > len([]rune(text)) {
		t.Fatalf("expected consumed runes within bounds, got %d", consumed)
	}
	if len([]rune(prefix)) > 18 {
		t.Fatalf("expected prefix to fit budget, got %d runes", len([]rune(prefix)))
	}
}

func TestOllamaEmbedBatchValidatesResponseCount(t *testing.T) {
	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		httpClient: newTestHTTPClient(t, http.StatusOK, ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{1, 0, 0}},
		}),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"one", "two"})
	if err == nil || !strings.Contains(err.Error(), "returned 1 embeddings for 2 inputs") {
		t.Fatalf("expected count validation error, got %v", err)
	}
}

func TestOllamaEmbedBatchValidatesDimensions(t *testing.T) {
	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		dims:    3,
		httpClient: newTestHTTPClient(t, http.StatusOK, ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{1, 0}},
		}),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"one"})
	if err == nil || !strings.Contains(err.Error(), "expected 3") {
		t.Fatalf("expected dimension validation error, got %v", err)
	}
}

func TestNewOllama_RespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, req.Context().Err()
		}),
	}

	_, err := newOllama(ctx, "http://ollama.test", "test-model", client)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}
}

func TestNewOllama_RejectsInvalidBaseURL(t *testing.T) {
	_, err := newOllama(context.Background(), "not-a-url", "test-model", &http.Client{})
	if err == nil || !strings.Contains(err.Error(), "embed URL") {
		t.Fatalf("expected embed URL validation error, got %v", err)
	}
}

func TestOllamaEmbedBatchMarksClientErrorsPermanent(t *testing.T) {
	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		httpClient: newTestHTTPClient(t, http.StatusBadRequest, map[string]string{
			"error": "the input length exceeds the context length",
		}),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"short input"})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
}
