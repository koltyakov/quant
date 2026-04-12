package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
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

func newProgrammableTestHTTPClient(t *testing.T, responder func(ollamaEmbedRequest) (int, any)) *http.Client {
	t.Helper()

	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("expected POST request, got %s", req.Method)
			}
			if req.URL.String() != "http://ollama.test/api/embed" {
				t.Fatalf("unexpected request URL: %s", req.URL.String())
			}

			var embedReq ollamaEmbedRequest
			if err := json.NewDecoder(req.Body).Decode(&embedReq); err != nil {
				t.Fatalf("decode request body: %v", err)
			}

			statusCode, body := responder(embedReq)
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal test response: %v", err)
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

func TestOllamaEmbedBatchAutomaticallyShrinksOversizedInput(t *testing.T) {
	var seen []int

	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		dims:    3,
		httpClient: newProgrammableTestHTTPClient(t, func(req ollamaEmbedRequest) (int, any) {
			if len(req.Input) != 1 {
				t.Fatalf("expected singleton request, got %d inputs", len(req.Input))
			}

			runes := utf8.RuneCountInString(req.Input[0])
			seen = append(seen, runes)
			if runes > 3000 {
				return http.StatusBadRequest, map[string]string{
					"error": "the input length exceeds the context length",
				}
			}

			return http.StatusOK, ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{{1, 0, 0}},
			}
		}),
	}

	vecs, err := o.EmbedBatch(context.Background(), []string{strings.Repeat("x", MaxInputRunes)})
	if err != nil {
		t.Fatalf("expected shrink-to-fit success, got %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("expected one embedding, got %#v", vecs)
	}
	if len(seen) < 2 {
		t.Fatalf("expected multiple attempts, saw %v", seen)
	}
	if seen[0] != MaxInputRunes {
		t.Fatalf("expected initial request at MaxInputRunes, saw %v", seen)
	}
	if seen[len(seen)-1] > 3000 {
		t.Fatalf("expected final request to fit backend budget, saw %v", seen)
	}
}

func TestOllamaEmbedBatchSplitsBatchBeforeShrinkingSingleton(t *testing.T) {
	var sawSingletonReduced bool

	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		dims:    3,
		httpClient: newProgrammableTestHTTPClient(t, func(req ollamaEmbedRequest) (int, any) {
			for _, input := range req.Input {
				if utf8.RuneCountInString(input) > 3000 {
					if len(req.Input) == 1 {
						sawSingletonReduced = true
					}
					return http.StatusBadRequest, map[string]string{
						"error": "the input length exceeds the context length",
					}
				}
			}

			embeddings := make([][]float32, len(req.Input))
			for i := range req.Input {
				embeddings[i] = []float32{1, 0, 0}
			}
			return http.StatusOK, ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: embeddings,
			}
		}),
	}

	texts := make([]string, 16)
	texts[0] = strings.Repeat("x", MaxInputRunes)
	for i := 1; i < len(texts); i++ {
		texts[i] = "short input"
	}

	vecs, err := o.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("expected split-and-shrink success, got %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("expected %d embeddings, got %d", len(texts), len(vecs))
	}
	if !sawSingletonReduced {
		t.Fatal("expected oversized input to be isolated to a singleton request before shrinking")
	}
}

func TestOllamaEmbedBatchMarksRetryBudgetExceededPermanent(t *testing.T) {
	o := &Ollama{
		baseURL: "http://ollama.test",
		model:   "test-model",
		httpClient: newTestHTTPClient(t, http.StatusInternalServerError, map[string]string{
			"error": "backend unavailable",
		}),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"short input"})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if !strings.Contains(err.Error(), "max retry budget") {
		t.Fatalf("expected max retry budget error, got %v", err)
	}
}
