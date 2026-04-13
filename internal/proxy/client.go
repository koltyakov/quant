package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/koltyakov/quant/internal/index"
)

type Client struct {
	addr       string
	httpClient *http.Client
}

func NewClient(addr string) *Client {
	return &Client{
		addr: "http://" + addr,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Addr() string {
	return c.addr
}

func (c *Client) Alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.addr+"/proxy/ping", nil) //nolint:gosec // addr is localhost from lock file or config
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // addr is localhost from lock file or config
	if err != nil {
		return false
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *Client) Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]index.SearchResult, error) {
	body := SearchRequest{
		Query:          query,
		QueryEmbedding: queryEmbedding,
		Limit:          limit,
		PathPrefix:     pathPrefix,
	}
	var resp SearchResponse
	if err := c.doPost(ctx, "/proxy/search", body, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *Client) FindSimilar(ctx context.Context, chunkID int64, limit int) ([]index.SearchResult, error) {
	body := FindSimilarRequest{ChunkID: chunkID, Limit: limit}
	var resp FindSimilarResponse
	if err := c.doPost(ctx, "/proxy/find_similar", body, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *Client) GetChunkByID(ctx context.Context, chunkID int64) (*index.SearchResult, error) {
	body := ChunkByIDRequest{ChunkID: chunkID}
	var resp ChunkByIDResponse
	if err := c.doPost(ctx, "/proxy/chunk_by_id", body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &resp.Chunk, nil
}

func (c *Client) GetDocumentByPath(_ context.Context, _ string) (*index.Document, error) {
	return nil, fmt.Errorf("GetDocumentByPath not available in proxy mode")
}

func (c *Client) ListDocuments(ctx context.Context) ([]index.Document, error) {
	return c.ListDocumentsLimit(ctx, 0)
}

func (c *Client) ListDocumentsLimit(ctx context.Context, limit int) ([]index.Document, error) {
	body := ListSourcesRequest{Limit: limit}
	var resp ListSourcesResponse
	if err := c.doPost(ctx, "/proxy/list_sources", body, &resp); err != nil {
		return nil, err
	}
	return resp.Documents, nil
}

func (c *Client) GetDocumentChunksByPath(_ context.Context, _ string) (map[string]index.ChunkRecord, error) {
	return nil, fmt.Errorf("GetDocumentChunksByPath not available in proxy mode")
}

func (c *Client) Stats(ctx context.Context) (int, int, error) {
	var resp StatsResponse
	if err := c.doGet(ctx, "/proxy/stats", &resp); err != nil {
		return 0, 0, err
	}
	return resp.DocCount, resp.ChunkCount, nil
}

func (c *Client) PingContext(ctx context.Context) error {
	if !c.Alive(ctx) {
		return fmt.Errorf("main process unreachable at %s", c.addr)
	}
	return nil
}

func (c *Client) doPost(ctx context.Context, path string, reqBody, respBody any) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	return c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(data), "application/json", respBody)
}

func (c *Client) doGet(ctx context.Context, path string, respBody any) error {
	return c.doRequest(ctx, http.MethodGet, path, nil, "", respBody)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.addr+path, body) //nolint:gosec // addr is localhost from lock file or config
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req) //nolint:gosec // addr is localhost from lock file or config
	if err != nil {
		return fmt.Errorf("proxy request to %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading proxy response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respData, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("proxy error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return fmt.Errorf("proxy error (%d): %s", resp.StatusCode, string(respData))
	}

	if err := json.Unmarshal(respData, respBody); err != nil {
		return fmt.Errorf("decoding proxy response: %w", err)
	}
	return nil
}
