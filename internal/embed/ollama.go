package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Ollama struct {
	baseURL    string
	embedURL   string
	model      string
	dims       int
	httpClient *http.Client
}

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

const MaxInputRunes = 4000

func NewOllama(ctx context.Context, baseURL, model string) (*Ollama, error) {
	return newOllama(ctx, baseURL, model, &http.Client{Timeout: 60 * time.Second})
}

func newOllama(ctx context.Context, baseURL, model string, httpClient *http.Client) (*Ollama, error) {
	embedURL, err := ollamaEmbedURL(baseURL)
	if err != nil {
		return nil, err
	}
	o := &Ollama{
		baseURL:    baseURL,
		embedURL:   embedURL,
		model:      model,
		httpClient: httpClient,
	}

	dims, err := o.probeDimensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("probing embedding dimensions: %w", err)
	}
	o.dims = dims

	return o, nil
}

func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return vecs[0], nil
}

const maxEmbedRetries = 4

func (o *Ollama) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return o.embedBatch(ctx, texts, 0)
}

func (o *Ollama) embedBatch(ctx context.Context, texts []string, retries int) ([][]float32, error) {
	if retries > maxEmbedRetries {
		return nil, fmt.Errorf("ollama: max retry budget (%d) exceeded", maxEmbedRetries)
	}

	truncated := make([]string, len(texts))
	for i, t := range texts {
		truncated[i] = TruncateForInput(t, MaxInputRunes)
	}

	reqBody := ollamaEmbedRequest{
		Model: o.model,
		Input: truncated,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	embedURL, err := o.requestURL()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embedURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	//nolint:gosec // Ollama endpoint is validated to an absolute http(s) URL before use.
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request to ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 400 {
			if len(truncated) > 1 {
				mid := len(truncated) / 2
				left, errL := o.embedBatch(ctx, truncated[:mid], retries+1)
				right, errR := o.embedBatch(ctx, truncated[mid:], retries+1)
				if errL != nil || errR != nil {
					return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
				}
				return append(left, right...), nil
			}
			rCount := utf8.RuneCountInString(truncated[0])
			if rCount > 64 {
				truncated[0] = TruncateForInput(truncated[0], rCount/2)
				return o.embedBatch(ctx, truncated, retries+1)
			}
		}
		// Retry transient server errors with exponential backoff.
		if resp.StatusCode >= 500 && retries < 4 {
			backoff := time.Duration(1<<retries) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			return o.embedBatch(ctx, texts, retries+1)
		}
		return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if len(embedResp.Embeddings) != len(truncated) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(embedResp.Embeddings), len(truncated))
	}
	for i, vec := range embedResp.Embeddings {
		if len(vec) == 0 {
			return nil, fmt.Errorf("ollama returned empty embedding at index %d", i)
		}
		if o.dims > 0 && len(vec) != o.dims {
			return nil, fmt.Errorf("ollama returned embedding with dimension %d, expected %d", len(vec), o.dims)
		}
	}

	return embedResp.Embeddings, nil
}

func (o *Ollama) Dimensions() int {
	return o.dims
}

func (o *Ollama) Close() error {
	o.httpClient.CloseIdleConnections()
	return nil
}

func (o *Ollama) probeDimensions(ctx context.Context) (int, error) {
	vecs, err := o.EmbedBatch(ctx, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("empty embedding returned from probe")
	}
	return len(vecs[0]), nil
}

func (o *Ollama) requestURL() (string, error) {
	if o.embedURL != "" {
		return o.embedURL, nil
	}
	return ollamaEmbedURL(o.baseURL)
}

func ollamaEmbedURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("embed URL must be a valid URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("embed URL must be an absolute URL with scheme and host")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("embed URL scheme must be http or https")
	}

	path := strings.TrimRight(parsed.Path, "/")
	embedURL := *parsed
	embedURL.Path = path + "/api/embed"
	embedURL.RawPath = ""
	embedURL.Fragment = ""
	return embedURL.String(), nil
}

// TruncateForInput cuts text to at most maxChars runes, preferring to break
// at a natural boundary (paragraph, sentence, clause, or word). It searches a
// 256-character window just before the cut point for the best break marker,
// falling back to a hard cut if no suitable boundary is found within the window.
// The window size balances break quality against preserving most of the allowed
// length - a larger window would find better breaks but discard more text.
func TruncateForInput(text string, maxChars int) string {
	prefix, _ := PrefixWithinInputBudget(text, maxChars)
	return prefix
}

// PrefixWithinInputBudget returns the largest leading segment that fits within
// maxChars runes, along with the number of original input runes consumed.
func PrefixWithinInputBudget(text string, maxChars int) (string, int) {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text, utf8.RuneCountInString(text)
	}

	runes := []rune(text)
	if len(runes) <= maxChars {
		return text, len(runes)
	}

	cut := maxChars
	windowStart := cut - 256
	if windowStart < 0 {
		windowStart = 0
	}
	window := string(runes[windowStart:cut])

	for _, marker := range []string{"\n\n", "\n", ". ", "! ", "? ", "; ", ", ", " "} {
		if idx := strings.LastIndex(window, marker); idx >= 0 {
			consumed := windowStart + idx + len([]rune(marker))
			candidate := strings.TrimSpace(string(runes[:consumed]))
			if utf8.RuneCountInString(candidate) >= maxChars/2 {
				return candidate, consumed
			}
		}
	}

	return strings.TrimSpace(string(runes[:cut])), cut
}
