package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type Ollama struct {
	baseURL    string
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

func NewOllama(baseURL, model string) (*Ollama, error) {
	o := &Ollama{
		baseURL:    baseURL,
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}

	dims, err := o.probeDimensions(context.Background())
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

func (o *Ollama) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return o.embedBatch(ctx, texts, 0)
}

func (o *Ollama) embedBatch(ctx context.Context, texts []string, depth int) ([][]float32, error) {
	if depth > 8 {
		return nil, fmt.Errorf("ollama: input too long after repeated truncation")
	}

	const maxChars = 4000
	truncated := make([]string, len(texts))
	for i, t := range texts {
		truncated[i] = truncateForEmbedding(t, maxChars)
	}

	reqBody := ollamaEmbedRequest{
		Model: o.model,
		Input: truncated,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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
				left, errL := o.embedBatch(ctx, truncated[:mid], depth)
				right, errR := o.embedBatch(ctx, truncated[mid:], depth)
				if errL != nil || errR != nil {
					return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
				}
				return append(left, right...), nil
			}
			rCount := utf8.RuneCountInString(truncated[0])
			if rCount > 64 {
				truncated[0] = truncateForEmbedding(truncated[0], rCount/2)
				return o.embedBatch(ctx, truncated, depth+1)
			}
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

func truncateForEmbedding(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}

	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}

	cut := maxChars
	windowStart := cut - 256
	if windowStart < 0 {
		windowStart = 0
	}
	window := string(runes[windowStart:cut])

	for _, marker := range []string{"\n\n", "\n", ". ", "! ", "? ", "; ", ", ", " "} {
		if idx := strings.LastIndex(window, marker); idx >= 0 {
			candidate := strings.TrimSpace(string(runes[:windowStart+idx+len(marker)]))
			if utf8.RuneCountInString(candidate) >= maxChars/2 {
				return candidate
			}
		}
	}

	return strings.TrimSpace(string(runes[:cut]))
}
