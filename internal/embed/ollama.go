package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

var ErrPermanent = errors.New("embed: permanent error")

// ErrOllamaUnavailable is returned when the Ollama server cannot be reached.
var ErrOllamaUnavailable = errors.New("embed: ollama server not reachable")

// ErrModelNotFound is returned when the requested model is not present on the Ollama server.
var ErrModelNotFound = errors.New("embed: model not found")

const MaxInputRunes = 4000
const minReducedInputRunes = 64

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
		if isOllamaConnectionError(err) {
			return nil, fmt.Errorf("%w at %s — is Ollama running? Start it with: ollama serve", ErrOllamaUnavailable, o.baseURL)
		}
		return nil, fmt.Errorf("sending request to ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w: model %q not found on Ollama — pull it with: ollama pull %s", ErrPermanent, ErrModelNotFound, o.model, o.model)
		}
		if resp.StatusCode == http.StatusBadRequest && shouldReduceInput(body) {
			// Batch splitting is structural recovery, not a transient retry. Keep the
			// retry budget available for server errors and singleton shrink attempts.
			if len(truncated) > 1 {
				mid := len(truncated) / 2
				left, errL := o.embedBatch(ctx, truncated[:mid], retries)
				right, errR := o.embedBatch(ctx, truncated[mid:], retries)
				if errL != nil {
					return nil, errL
				}
				if errR != nil {
					return nil, errR
				}
				return append(left, right...), nil
			}
			if reduced, ok := reduceInputForContext(truncated[0]); ok {
				return o.embedBatch(ctx, []string{reduced}, retries)
			}
		}
		// Retry transient server errors with exponential backoff.
		if resp.StatusCode >= 500 {
			if retries >= maxEmbedRetries {
				return nil, fmt.Errorf("%w: ollama: max retry budget (%d) exceeded", ErrPermanent, maxEmbedRetries)
			}
			backoff := time.Duration(1<<retries) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			return o.embedBatch(ctx, texts, retries+1)
		}
		return nil, ollamaStatusError(resp.StatusCode, body)
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

// isOllamaConnectionError reports whether err indicates that the Ollama server
// could not be reached at all (dial failure or DNS resolution failure).
func isOllamaConnectionError(err error) bool {
	var netErr *net.OpError
	if errors.As(err, &netErr) && netErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func ollamaStatusError(statusCode int, body []byte) error {
	if statusCode >= 400 && statusCode < 500 && statusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%w: ollama returned status %d: %s", ErrPermanent, statusCode, string(body))
	}
	return fmt.Errorf("ollama returned status %d: %s", statusCode, string(body))
}

func shouldReduceInput(body []byte) bool {
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "input length") ||
		strings.Contains(msg, "context length") ||
		strings.Contains(msg, "context window") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "prompt is too long")
}

func reduceInputForContext(text string) (string, bool) {
	rCount := utf8.RuneCountInString(text)
	if rCount <= minReducedInputRunes {
		return "", false
	}

	reduced := TruncateForInput(text, rCount/2)
	if utf8.RuneCountInString(reduced) >= rCount {
		runes := []rune(text)
		reduced = strings.TrimSpace(string(runes[:rCount/2]))
	}
	if utf8.RuneCountInString(reduced) >= rCount || reduced == "" {
		return "", false
	}
	return reduced, true
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
