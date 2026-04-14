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

type OpenAICompatible struct {
	baseURL    string
	model      string
	apiKey     string
	dims       int
	httpClient *http.Client
}

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func NewOpenAICompatible(ctx context.Context, baseURL, model, apiKey string) (*OpenAICompatible, error) {
	return newOpenAICompatible(ctx, baseURL, model, apiKey, &http.Client{Timeout: 60 * time.Second})
}

func newOpenAICompatible(ctx context.Context, baseURL, model, apiKey string, httpClient *http.Client) (*OpenAICompatible, error) {
	embedURL, err := openAIEmbedURL(baseURL)
	if err != nil {
		return nil, err
	}
	o := &OpenAICompatible{
		baseURL:    embedURL,
		model:      model,
		apiKey:     apiKey,
		httpClient: httpClient,
	}
	dims, err := o.probeDimensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("probing embedding dimensions: %w", err)
	}
	o.dims = dims
	return o, nil
}

func (o *OpenAICompatible) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return vecs[0], nil
}

func (o *OpenAICompatible) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	truncated := make([]string, len(texts))
	for i, t := range texts {
		truncated[i] = TruncateForInput(t, MaxInputRunes)
	}

	reqBody := openAIEmbedRequest{
		Model: o.model,
		Input: truncated,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, fmt.Errorf("%w: openai-compatible returned status %d: %s", ErrPermanent, resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("openai-compatible returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp openAIEmbedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if len(embedResp.Data) != len(truncated) {
		return nil, fmt.Errorf("returned %d embeddings for %d inputs", len(embedResp.Data), len(truncated))
	}

	result := make([][]float32, len(truncated))
	for _, d := range embedResp.Data {
		if d.Index < 0 || d.Index >= len(result) {
			return nil, fmt.Errorf("embedding index %d out of range", d.Index)
		}
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("empty embedding at index %d", d.Index)
		}
		result[d.Index] = d.Embedding
	}

	for i, vec := range result {
		if o.dims > 0 && len(vec) != o.dims {
			return nil, fmt.Errorf("embedding dimension %d at index %d, expected %d", len(vec), i, o.dims)
		}
	}

	return result, nil
}

func (o *OpenAICompatible) Dimensions() int {
	return o.dims
}

func (o *OpenAICompatible) Close() error {
	o.httpClient.CloseIdleConnections()
	return nil
}

func (o *OpenAICompatible) probeDimensions(ctx context.Context) (int, error) {
	vecs, err := o.EmbedBatch(ctx, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("empty embedding returned from probe")
	}
	return len(vecs[0]), nil
}

func openAIEmbedURL(raw string) (string, error) {
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
	if !strings.HasSuffix(path, "/embeddings") && !strings.HasSuffix(path, "/embed") {
		path += "/v1/embeddings"
	}
	embedURL := *parsed
	embedURL.Path = path
	embedURL.RawPath = ""
	embedURL.Fragment = ""
	return embedURL.String(), nil
}

func init() {
	_ = utf8.RuneCountInString
}
