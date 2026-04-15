package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestOllamaDimensions(t *testing.T) {
	t.Parallel()

	o := &Ollama{dims: 768}
	if got := o.Dimensions(); got != 768 {
		t.Fatalf("Dimensions() = %d, want 768", got)
	}
}

func TestOllamaClose(t *testing.T) {
	t.Parallel()

	o := &Ollama{dims: 3, httpClient: &http.Client{}}
	if err := o.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOllamaEmbedURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "basic http", raw: "http://localhost:11434", want: "http://localhost:11434/api/embed"},
		{name: "basic https", raw: "https://ollama.example.com", want: "https://ollama.example.com/api/embed"},
		{name: "trailing slash stripped", raw: "http://localhost:11434/", want: "http://localhost:11434/api/embed"},
		{name: "path preserved", raw: "http://host/ollama", want: "http://host/ollama/api/embed"},
		{name: "empty string", raw: "", wantErr: "absolute URL"},
		{name: "relative path", raw: "/api/embed", wantErr: "absolute URL"},
		{name: "ftp scheme rejected", raw: "ftp://host", wantErr: "scheme must be http or https"},
		{name: "whitespace trimmed", raw: "  http://localhost:11434  ", want: "http://localhost:11434/api/embed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ollamaEmbedURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ollamaEmbedURL(%q) error = %v, want substring %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ollamaEmbedURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ollamaEmbedURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestOllamaEmbedURLRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	_, err := ollamaEmbedURL("http://[::1]:namedport")
	if err == nil {
		t.Fatal("expected error for invalid URL port")
	}
}

func TestIsOllamaConnectionError(t *testing.T) {
	t.Parallel()

	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if !isOllamaConnectionError(dialErr) {
		t.Fatalf("expected dial net.OpError to be connection error")
	}

	dnsErr := &net.DNSError{Err: "no such host", Name: "ollama.test"}
	if !isOllamaConnectionError(dnsErr) {
		t.Fatalf("expected DNSError to be connection error")
	}

	readErr := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}
	if isOllamaConnectionError(readErr) {
		t.Fatalf("expected read net.OpError to not be connection error")
	}

	if isOllamaConnectionError(errors.New("generic")) {
		t.Fatalf("expected generic error to not be connection error")
	}

	wrappedDial := fmt.Errorf("wrapped: %w", dialErr)
	if !isOllamaConnectionError(wrappedDial) {
		t.Fatalf("expected wrapped dial error to be connection error")
	}
}

func TestReduceInputForContext(t *testing.T) {
	t.Parallel()

	t.Run("short input returns false", func(t *testing.T) {
		t.Parallel()

		short := "hi"
		_, ok := reduceInputForContext(short)
		if ok {
			t.Fatal("expected reduceInputForContext to return false for short input")
		}
	})

	t.Run("reduces long input", func(t *testing.T) {
		t.Parallel()

		long := strings.Repeat("word ", 2000)
		reduced, ok := reduceInputForContext(long)
		if !ok {
			t.Fatal("expected reduceInputForContext to return true for long input")
		}
		if len(reduced) >= len(long) {
			t.Fatalf("expected reduced to be shorter, got %d >= %d", len(reduced), len(long))
		}
		if reduced == "" {
			t.Fatal("expected non-empty reduced output")
		}
	})

	t.Run("exactly at minimum returns false", func(t *testing.T) {
		t.Parallel()

		exactlyMin := strings.Repeat("x", minReducedInputRunes)
		_, ok := reduceInputForContext(exactlyMin)
		if ok {
			t.Fatalf("expected false for input at minimum size (%d runes)", minReducedInputRunes)
		}
	})

	t.Run("slightly above minimum", func(t *testing.T) {
		t.Parallel()

		text := strings.Repeat("x", minReducedInputRunes+10)
		reduced, ok := reduceInputForContext(text)
		if !ok {
			t.Fatal("expected reduction to succeed")
		}
		if reduced == "" {
			t.Fatal("expected non-empty output")
		}
	})
}

func TestOllamaStatusError(t *testing.T) {
	t.Parallel()

	t.Run("4xx is permanent", func(t *testing.T) {
		t.Parallel()

		err := ollamaStatusError(http.StatusForbidden, []byte("forbidden"))
		if !errors.Is(err, ErrPermanent) {
			t.Fatalf("expected ErrPermanent, got %v", err)
		}
	})

	t.Run("429 is not permanent", func(t *testing.T) {
		t.Parallel()

		err := ollamaStatusError(http.StatusTooManyRequests, []byte("rate limited"))
		if errors.Is(err, ErrPermanent) {
			t.Fatalf("expected 429 to not be permanent, got %v", err)
		}
	})

	t.Run("5xx is not permanent", func(t *testing.T) {
		t.Parallel()

		err := ollamaStatusError(http.StatusInternalServerError, []byte("server error"))
		if errors.Is(err, ErrPermanent) {
			t.Fatalf("expected 5xx to not be permanent, got %v", err)
		}
	})

	t.Run("error message contains status and body", func(t *testing.T) {
		t.Parallel()

		err := ollamaStatusError(http.StatusNotFound, []byte("model missing"))
		if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model missing") {
			t.Fatalf("expected error to contain status and body, got %v", err)
		}
	})
}

func TestShouldReduceInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		body string
		want bool
	}{
		{body: "input length exceeds", want: true},
		{body: "the context length exceeded", want: true},
		{body: "context window exceeded", want: true},
		{body: "too many tokens in prompt", want: true},
		{body: "prompt is too long for model", want: true},
		{body: "INPUT LENGTH EXCEEDS", want: true},
		{body: "Context Length was exceeded", want: true},
		{body: "something else entirely", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			t.Parallel()

			got := shouldReduceInput([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("shouldReduceInput(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestOllamaEmbedBatchReturnsEmptyForEmptyInput(t *testing.T) {
	t.Parallel()

	o := &Ollama{dims: 3, httpClient: &http.Client{}}
	vecs, err := o.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil) error = %v", err)
	}
	if vecs != nil {
		t.Fatalf("EmbedBatch(nil) = %#v, want nil", vecs)
	}
}

func TestOllamaEmbedSingleText(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var embedReq ollamaEmbedRequest
			if err := json.NewDecoder(req.Body).Decode(&embedReq); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(embedReq.Input) != 1 || embedReq.Input[0] != "hello" {
				t.Fatalf("expected single input 'hello', got %v", embedReq.Input)
			}
			resp := ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{{0.1, 0.2, 0.3}},
			}
			payload, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(payload))),
				Request:    req,
			}, nil
		}),
	}

	o := &Ollama{
		baseURL:    "http://ollama.test",
		embedURL:   "http://ollama.test/api/embed",
		model:      "test-model",
		dims:       3,
		httpClient: client,
	}

	vec, err := o.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Fatalf("Embed() = %v, want [0.1 0.2 0.3]", vec)
	}
}

func TestOllamaRequestURLUsesEmbedURL(t *testing.T) {
	t.Parallel()

	o := &Ollama{embedURL: "http://custom.test/api/embed"}
	got, err := o.requestURL()
	if err != nil {
		t.Fatalf("requestURL() error = %v", err)
	}
	if got != "http://custom.test/api/embed" {
		t.Fatalf("requestURL() = %q, want %q", got, "http://custom.test/api/embed")
	}
}

func TestOllamaRequestURLFallback(t *testing.T) {
	t.Parallel()

	o := &Ollama{baseURL: "http://ollama.test"}
	got, err := o.requestURL()
	if err != nil {
		t.Fatalf("requestURL() error = %v", err)
	}
	if got != "http://ollama.test/api/embed" {
		t.Fatalf("requestURL() = %q, want %q", got, "http://ollama.test/api/embed")
	}
}

func TestOllamaReturnsEmptyEmbedding(t *testing.T) {
	t.Parallel()

	o := &Ollama{
		baseURL:  "http://ollama.test",
		embedURL: "http://ollama.test/api/embed",
		model:    "test-model",
		dims:     3,
		httpClient: newTestHTTPClient(t, http.StatusOK, ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{}},
		}),
	}

	_, err := o.EmbedBatch(context.Background(), []string{"hello"})
	if err == nil || !strings.Contains(err.Error(), "empty embedding") {
		t.Fatalf("expected empty embedding error, got %v", err)
	}
}

func TestNewOllamaValidatesBaseURLBeforeProbing(t *testing.T) {
	t.Parallel()

	_, err := newOllama(context.Background(), "ftp://bad.host", "test", &http.Client{})
	if err == nil {
		t.Fatal("expected error for ftp scheme")
	}
	if !strings.Contains(err.Error(), "embed URL") {
		t.Fatalf("expected embed URL validation error, got %v", err)
	}
}

func TestNewOllamaSuccess(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{{1.0, 2.0, 3.0}},
			}
			payload, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(payload))),
				Request:    req,
			}, nil
		}),
	}

	o, err := newOllama(context.Background(), "http://ollama.test", "test-model", client)
	if err != nil {
		t.Fatalf("newOllama() error = %v", err)
	}
	if o.Dimensions() != 3 {
		t.Fatalf("Dimensions() = %d, want 3", o.Dimensions())
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenAICompatibleDimensions(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{dims: 1536}
	if got := o.Dimensions(); got != 1536 {
		t.Fatalf("Dimensions() = %d, want 1536", got)
	}
}

func TestOpenAICompatibleClose(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{dims: 3, httpClient: &http.Client{}}
	if err := o.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenAICompatibleEmbedSingleText(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		resp := `{"data":[{"embedding":[0.5,0.6,0.7],"index":0}],"model":"test"}`
		return testHTTPResponse(http.StatusOK, resp), nil
	})}

	o := &OpenAICompatible{
		baseURL:    "https://api.example.com/v1/embeddings",
		model:      "test-model",
		apiKey:     "test-key",
		dims:       3,
		httpClient: client,
	}

	vec, err := o.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.5 {
		t.Fatalf("Embed() = %v, want [0.5 0.6 0.7]", vec)
	}
}

func TestOpenAICompatibleNoAuthWhenNoAPIKey(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("expected no Authorization header, got %q", got)
		}
		resp := `{"data":[{"embedding":[1,2],"index":0}],"model":"test"}`
		return testHTTPResponse(http.StatusOK, resp), nil
	})}

	o := &OpenAICompatible{
		baseURL:    "https://api.example.com/v1/embeddings",
		model:      "test-model",
		apiKey:     "",
		dims:       2,
		httpClient: client,
	}

	_, err := o.EmbedBatch(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
}

func TestOpenAICompatibleClientErrorIsPermanent(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{
		baseURL: "https://api.example.com/v1/embeddings",
		model:   "test-model",
		apiKey:  "key",
		dims:    3,
		httpClient: &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusForbidden, "forbidden"), nil
		})},
	}

	_, err := o.EmbedBatch(context.Background(), []string{"hello"})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected ErrPermanent for 4xx, got %v", err)
	}
}

func TestOpenAICompatibleServerErrorIsNotPermanent(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{
		baseURL: "https://api.example.com/v1/embeddings",
		model:   "test-model",
		apiKey:  "key",
		dims:    3,
		httpClient: &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusBadGateway, "bad gateway"), nil
		})},
	}

	_, err := o.EmbedBatch(context.Background(), []string{"hello"})
	if errors.Is(err, ErrPermanent) {
		t.Fatalf("expected non-permanent error for 5xx, got %v", err)
	}
	if !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("expected status in error, got %v", err)
	}
}

func TestOpenAICompatibleConnectionError(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{
		baseURL: "https://api.example.com/v1/embeddings",
		model:   "test-model",
		apiKey:  "key",
		dims:    3,
		httpClient: &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})},
	}

	_, err := o.EmbedBatch(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "sending request") {
		t.Fatalf("expected 'sending request' in error, got %v", err)
	}
}

func TestOpenAIEmbedURLWithExistingEmbedPath(t *testing.T) {
	t.Parallel()

	got, err := openAIEmbedURL("https://host.example.com/v1/embeddings")
	if err != nil {
		t.Fatalf("openAIEmbedURL() error = %v", err)
	}
	if got != "https://host.example.com/v1/embeddings" {
		t.Fatalf("openAIEmbedURL() = %q, want %q", got, "https://host.example.com/v1/embeddings")
	}
}

func TestOpenAIEmbedURLWithExistingEmbedAltPath(t *testing.T) {
	t.Parallel()

	got, err := openAIEmbedURL("https://host.example.com/custom/embed")
	if err != nil {
		t.Fatalf("openAIEmbedURL() error = %v", err)
	}
	if got != "https://host.example.com/custom/embed" {
		t.Fatalf("openAIEmbedURL() = %q, want %q", got, "https://host.example.com/custom/embed")
	}
}

func TestNewEmbedderWithOllamaProvider(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{{1.0, 2.0}},
			}
			payload, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(payload))),
				Request:    req,
			}, nil
		}),
	}

	embedder, err := newOllama(context.Background(), "http://ollama.test", "test-model", client)
	if err != nil {
		t.Fatalf("NewEmbedder(ollama) error = %v", err)
	}
	_ = embedder
}

func TestNewEmbedderWithOpenAIProvider(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := `{"data":[{"embedding":[1,2],"index":0}],"model":"test"}`
		return testHTTPResponse(http.StatusOK, resp), nil
	})}

	embedder, err := newOpenAICompatible(context.Background(), "https://api.example.com", "test-model", "key", client)
	if err != nil {
		t.Fatalf("newOpenAICompatible() error = %v", err)
	}
	if embedder.Dimensions() != 2 {
		t.Fatalf("Dimensions() = %d, want 2", embedder.Dimensions())
	}
}

func TestNewEmbedderUnknownProviderDefault(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{{1.0}},
			}
			payload, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(payload))),
				Request:    req,
			}, nil
		}),
	}

	embedder, err := newOllama(context.Background(), "http://ollama.test", "test-model", client)
	if err != nil {
		t.Fatalf("newOllama() error = %v", err)
	}
	if embedder.Dimensions() != 1 {
		t.Fatalf("Dimensions() = %d, want 1", embedder.Dimensions())
	}
}

func TestDetectProviderDefaults(t *testing.T) {
	t.Parallel()

	if got := DetectProvider("http://localhost:11434"); got != ProviderOllama {
		t.Fatalf("DetectProvider(localhost) = %q, want %q", got, ProviderOllama)
	}
	if got := DetectProvider("http://some-other-host.com"); got != ProviderOllama {
		t.Fatalf("DetectProvider(other) = %q, want %q (default)", got, ProviderOllama)
	}
}

func TestOllamaEmbedReturnsErrorOnEmptyBatch(t *testing.T) {
	t.Parallel()

	o := &Ollama{
		embedURL: "http://ollama.test/api/embed",
		model:    "test-model",
		dims:     3,
		httpClient: newProgrammableTestHTTPClient(t, func(req ollamaEmbedRequest) (int, any) {
			return http.StatusOK, ollamaEmbedResponse{
				Model:      "test-model",
				Embeddings: [][]float32{},
			}
		}),
	}

	_, err := o.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when no embedding returned")
	}
}

func TestOllamaProbeDimensionError(t *testing.T) {
	t.Parallel()

	o := &Ollama{
		embedURL: "http://ollama.test/api/embed",
		model:    "test-model",
		httpClient: newTestHTTPClient(t, http.StatusOK, ollamaEmbedResponse{
			Model:      "test-model",
			Embeddings: [][]float32{{}},
		}),
	}

	_, err := o.probeDimensions(context.Background())
	if err == nil {
		t.Fatal("expected error for empty embedding in probe")
	}
}

func TestOpenAICompatibleProbeDimensionError(t *testing.T) {
	t.Parallel()

	o := &OpenAICompatible{
		baseURL: "https://api.example.com/v1/embeddings",
		model:   "test-model",
		apiKey:  "key",
		dims:    0,
		httpClient: &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusOK, `{"data":[{"embedding":[],"index":0}],"model":"test"}`), nil
		})},
	}

	_, err := o.probeDimensions(context.Background())
	if err == nil {
		t.Fatal("expected error for empty embedding in probe")
	}
}
