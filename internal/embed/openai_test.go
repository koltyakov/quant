package embed

import (
	"context"
	stderrors "errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type openAIRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openAIRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOpenAIEmbedURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "base url appends embeddings", raw: "https://api.example.com", want: "https://api.example.com/v1/embeddings"},
		{name: "trim trailing slash", raw: "https://api.example.com/", want: "https://api.example.com/v1/embeddings"},
		{name: "existing embeddings path", raw: "https://api.example.com/custom/embeddings", want: "https://api.example.com/custom/embeddings"},
		{name: "existing embed path", raw: "https://api.example.com/custom/embed", want: "https://api.example.com/custom/embed"},
		{name: "invalid absolute url", raw: "/relative", wantErr: "absolute URL"},
		{name: "invalid scheme", raw: "ftp://api.example.com", wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := openAIEmbedURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("openAIEmbedURL(%q) error = %v, want substring %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("openAIEmbedURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("openAIEmbedURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNewOpenAICompatibleAndEmbedMethods(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/v1/embeddings")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization header = %q, want %q", got, "Bearer secret")
		}

		switch requests {
		case 1:
			return testHTTPResponse(http.StatusOK, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"test"}`), nil
		case 2:
			return testHTTPResponse(http.StatusOK, `{"data":[{"embedding":[3,4,5],"index":1},{"embedding":[1,2,3],"index":0}],"model":"test"}`), nil
		case 3:
			return testHTTPResponse(http.StatusOK, `{"data":[{"embedding":[9,8,7],"index":0}],"model":"test"}`), nil
		default:
			t.Fatalf("unexpected request count %d", requests)
			return nil, nil
		}
	})}

	embedder, err := newOpenAICompatible(context.Background(), "https://api.example.com", "test-model", "secret", client)
	if err != nil {
		t.Fatalf("NewOpenAICompatible() error = %v", err)
	}
	defer func() {
		if err := embedder.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	if got := embedder.Dimensions(); got != 3 {
		t.Fatalf("Dimensions() = %d, want 3", got)
	}

	batch, err := embedder.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if len(batch) != 2 || len(batch[0]) != 3 || batch[0][0] != 1 || batch[1][0] != 3 {
		t.Fatalf("EmbedBatch() = %#v, want reordered vectors", batch)
	}

	vec, err := embedder.Embed(context.Background(), "single")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 3 || vec[0] != 9 {
		t.Fatalf("Embed() = %#v, want [9 8 7]", vec)
	}

	if requests != 3 {
		t.Fatalf("request count = %d, want 3", requests)
	}
}

func TestOpenAICompatibleEmbedBatchErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "permanent client error", status: http.StatusBadRequest, body: `bad request`, wantErr: ErrPermanent.Error()},
		{name: "server error", status: http.StatusBadGateway, body: `gateway`, wantErr: "status 502"},
		{name: "decode error", status: http.StatusOK, body: `{"data":`, wantErr: "decoding response"},
		{name: "mismatched result count", status: http.StatusOK, body: `{"data":[]}`, wantErr: "returned 0 embeddings for 1 inputs"},
		{name: "index out of range", status: http.StatusOK, body: `{"data":[{"embedding":[1,2,3],"index":1}]}`, wantErr: "index 1 out of range"},
		{name: "empty embedding", status: http.StatusOK, body: `{"data":[{"embedding":[],"index":0}]}`, wantErr: "empty embedding at index 0"},
		{name: "dimension mismatch", status: http.StatusOK, body: `{"data":[{"embedding":[1,2],"index":0}]}`, wantErr: "expected 3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedder := &OpenAICompatible{
				baseURL: "https://api.example.com/v1/embeddings",
				model:   "test-model",
				apiKey:  "secret",
				dims:    3,
				httpClient: &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return testHTTPResponse(tt.status, tt.body), nil
				})},
			}

			_, err := embedder.EmbedBatch(context.Background(), []string{"hello"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("EmbedBatch() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpenAICompatibleEmbedBatchEmptyInput(t *testing.T) {
	t.Parallel()

	embedder := &OpenAICompatible{dims: 3, httpClient: &http.Client{}}
	vecs, err := embedder.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil) error = %v", err)
	}
	if vecs != nil {
		t.Fatalf("EmbedBatch(nil) = %#v, want nil", vecs)
	}
}

func TestDetectProviderAndUnsupportedProviderError(t *testing.T) {
	t.Parallel()

	if got := DetectProvider("https://api.openai.com/v1/embeddings"); got != ProviderOpenAI {
		t.Fatalf("DetectProvider(openai) = %q, want %q", got, ProviderOpenAI)
	}
	if got := DetectProvider("http://localhost:11434/api/embed"); got != ProviderOllama {
		t.Fatalf("DetectProvider(ollama) = %q, want %q", got, ProviderOllama)
	}

	_, err := NewEmbedder(context.Background(), ProviderType("custom"), "http://example.com", "model", "")
	if err == nil || !strings.Contains(err.Error(), "unsupported embed provider") {
		t.Fatalf("NewEmbedder(custom) error = %v, want unsupported provider error", err)
	}
}

func TestNewOpenAICompatibleProbeError(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: openAIRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testHTTPResponse(http.StatusUnauthorized, "nope"), nil
	})}

	_, err := newOpenAICompatible(context.Background(), "https://api.example.com", "test-model", "secret", client)
	if err == nil {
		t.Fatal("NewOpenAICompatible() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "probing embedding dimensions") {
		t.Fatalf("NewOpenAICompatible() error = %v, want probe error", err)
	}
	if !strings.Contains(err.Error(), ErrPermanent.Error()) {
		t.Fatalf("NewOpenAICompatible() error = %v, want permanent error", err)
	}
}

func TestErrPermanentSupportsErrorsIs(t *testing.T) {
	t.Parallel()

	err := stderrors.Join(ErrPermanent, stderrors.New("wrapped"))
	if !stderrors.Is(err, ErrPermanent) {
		t.Fatalf("errors.Is(%v, ErrPermanent) = false, want true", err)
	}
}
