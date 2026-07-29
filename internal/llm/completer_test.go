package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNewCompleterUsesExplicitProvider(t *testing.T) {
	t.Parallel()

	openAICompleter, err := NewCompleter(Config{
		Provider: ProviderOpenAI,
		BaseURL:  "https://proxy.internal.example.com/openai",
	})
	if err != nil {
		t.Fatalf("NewCompleter(openai) error = %v", err)
	}
	if _, ok := openAICompleter.(*OpenAICompleter); !ok {
		t.Fatalf("expected explicit openai provider to return OpenAICompleter, got %T", openAICompleter)
	}

	ollamaCompleter, err := NewCompleter(Config{
		Provider: ProviderOllama,
		BaseURL:  "http://localhost:11434",
		APIKey:   "proxy-token",
	})
	if err != nil {
		t.Fatalf("NewCompleter(ollama) error = %v", err)
	}
	if _, ok := ollamaCompleter.(*OllamaCompleter); !ok {
		t.Fatalf("expected explicit ollama provider to return OllamaCompleter, got %T", ollamaCompleter)
	}
}

func TestNewCompleterAutoDetectsKnownHosts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		baseURL string
		want    ProviderType
	}{
		{name: "localhost -> ollama", baseURL: "http://localhost:11434", want: ProviderOllama},
		{name: "openai host -> openai", baseURL: "https://api.openai.com", want: ProviderOpenAI},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			completer, err := NewCompleter(Config{BaseURL: tt.baseURL})
			if err != nil {
				t.Fatalf("NewCompleter(%q) error = %v", tt.baseURL, err)
			}
			switch tt.want {
			case ProviderOllama:
				if _, ok := completer.(*OllamaCompleter); !ok {
					t.Fatalf("expected OllamaCompleter, got %T", completer)
				}
			case ProviderOpenAI:
				if _, ok := completer.(*OpenAICompleter); !ok {
					t.Fatalf("expected OpenAICompleter, got %T", completer)
				}
			}
		})
	}
}

func TestNewCompleterRejectsUnknownHostWithoutProvider(t *testing.T) {
	t.Parallel()

	_, err := NewCompleter(Config{BaseURL: "https://llm.example.com"})
	if err == nil || !strings.Contains(err.Error(), "llm_provider not set") {
		t.Fatalf("expected llm_provider error, got %v", err)
	}
}

func TestOpenAIChatURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "base url appends chat", raw: "https://api.example.com", want: "https://api.example.com/v1/chat/completions"},
		{name: "v1 base stays v1 chat", raw: "https://api.example.com/v1", want: "https://api.example.com/v1/chat/completions"},
		{name: "existing chat path preserved", raw: "https://api.example.com/custom/chat/completions", want: "https://api.example.com/custom/chat/completions"},
		{name: "embedding path rewrites to chat", raw: "https://api.example.com/v1/embeddings", want: "https://api.example.com/v1/chat/completions"},
		{name: "invalid scheme", raw: "ftp://api.example.com", wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := openAIChatURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("openAIChatURL(%q) error = %v, want substring %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("openAIChatURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("openAIChatURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestOllamaChatURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "base url appends api chat", raw: "http://localhost:11434", want: "http://localhost:11434/api/chat"},
		{name: "api base stays api chat", raw: "http://localhost:11434/api", want: "http://localhost:11434/api/chat"},
		{name: "existing chat path preserved", raw: "http://localhost:11434/api/chat", want: "http://localhost:11434/api/chat"},
		{name: "embed path rewrites to chat", raw: "http://localhost:11434/api/embed", want: "http://localhost:11434/api/chat"},
		{name: "invalid scheme", raw: "ftp://localhost:11434", wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ollamaChatURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ollamaChatURL(%q) error = %v, want substring %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ollamaChatURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ollamaChatURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestOpenAICompleterUsesNormalizedChatURL(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization header = %q, want %q", got, "Bearer secret")
		}
		return testHTTPResponse(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`), nil
	})}

	completer, err := newOpenAICompleter(Config{BaseURL: "https://api.example.com/v1", APIKey: "secret"}, client)
	if err != nil {
		t.Fatalf("newOpenAICompleter() error = %v", err)
	}

	resp, err := completer.Complete(context.Background(), CompleteRequest{
		Model:    "gpt-4.1-mini",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Complete() content = %q, want %q", resp.Content, "ok")
	}
}

func TestOllamaCompleterUsesNormalizedChatURL(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/api/chat")
		}
		return testHTTPResponse(http.StatusOK, `{"message":{"content":"ok"}}`), nil
	})}

	completer, err := newOllamaCompleter(Config{BaseURL: "http://localhost:11434/api"}, client)
	if err != nil {
		t.Fatalf("newOllamaCompleter() error = %v", err)
	}

	resp, err := completer.Complete(context.Background(), CompleteRequest{
		Model:    "llama3.2",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Complete() content = %q, want %q", resp.Content, "ok")
	}
}

func TestCompleterDoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return testHTTPResponse(http.StatusNotFound, `model not found`), nil
	})}

	completer, err := newOllamaCompleter(Config{BaseURL: "http://localhost:11434", MaxRetries: 3}, client)
	if err != nil {
		t.Fatalf("newOllamaCompleter() error = %v", err)
	}
	_, err = completer.Complete(context.Background(), CompleteRequest{
		Model:    "missing",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("permanent error retried: got %d calls, want 1", calls)
	}
}

func TestCompleterRetriesTransientErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return testHTTPResponse(http.StatusBadGateway, `bad gateway`), nil
		}
		return testHTTPResponse(http.StatusOK, `{"message":{"content":"ok"}}`), nil
	})}

	completer, err := newOllamaCompleter(Config{BaseURL: "http://localhost:11434", MaxRetries: 3}, client)
	if err != nil {
		t.Fatalf("newOllamaCompleter() error = %v", err)
	}
	resp, err := completer.Complete(context.Background(), CompleteRequest{
		Model:    "llama3.2",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "ok" || calls != 3 {
		t.Fatalf("expected success on third attempt, got content=%q calls=%d", resp.Content, calls)
	}
}

func TestCompleterDefaultMaxRetries(t *testing.T) {
	t.Parallel()

	completer, err := NewOllamaCompleter(Config{BaseURL: "http://localhost:11434"})
	if err != nil {
		t.Fatalf("NewOllamaCompleter() error = %v", err)
	}
	if completer.maxRetries != 2 {
		t.Fatalf("default maxRetries = %d, want 2", completer.maxRetries)
	}
}

func TestInferProviderRejectsLookalikeHosts(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://openai.com.evil.example/v1",
		"https://notopenai.com/v1",
	} {
		if _, err := inferProvider(raw); err == nil {
			t.Fatalf("inferProvider(%q) unexpectedly inferred a provider", raw)
		}
	}
	if got, err := inferProvider("https://api.openai.com/v1"); err != nil || got != ProviderOpenAI {
		t.Fatalf("inferProvider(api.openai.com) = %q, %v; want %q, nil", got, err, ProviderOpenAI)
	}
}
