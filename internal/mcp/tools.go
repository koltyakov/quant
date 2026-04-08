package mcp

import (
	"context"
	"fmt"
	"os"

	"github.com/andrew/quant/internal/index"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerTools() {
	s.mcp.AddTool(mcplib.NewTool("search",
		mcplib.WithDescription("Semantic search across indexed documents"),
		mcplib.WithString("query",
			mcplib.Required(),
			mcplib.Description("Search query text"),
		),
		mcplib.WithNumber("limit",
			mcplib.Description("Maximum number of results (default: 5)"),
		),
		mcplib.WithNumber("threshold",
			mcplib.Description("Minimum result score. Vector-only results use cosine-style scores; hybrid FTS+vector results use RRF ranks (default: 0)"),
		),
		mcplib.WithString("path",
			mcplib.Description("Filter results to documents whose path starts with this prefix"),
		),
	), s.handleSearch)

	s.mcp.AddTool(mcplib.NewTool("list_sources",
		mcplib.WithDescription("List indexed documents"),
	), s.handleListSources)

	s.mcp.AddTool(mcplib.NewTool("index_status",
		mcplib.WithDescription("Get index statistics: total docs, chunks, DB size"),
	), s.handleIndexStatus)
}

func (s *Server) handleSearch(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := request.GetArguments()

	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query is required")
	}

	limit := 5
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}

	threshold := float32(0)
	if v, ok := args["threshold"].(float64); ok {
		threshold = float32(v)
	}

	pathPrefix := ""
	if v, ok := args["path"].(string); ok {
		pathPrefix = v
	}

	queryEmbedding, err := s.cachedEmbed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	results, err := s.store.Search(ctx, query, queryEmbedding, limit, pathPrefix)
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}

	var filtered []index.SearchResult
	for _, r := range results {
		if r.Score >= threshold {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) == 0 {
		return mcplib.NewToolResultText("No results found."), nil
	}

	output := ""
	for i, r := range filtered {
		output += fmt.Sprintf("--- Result %d (score: %.4f, kind: %s) ---\nFile: %s (chunk %d)\n%s\n\n",
			i+1, r.Score, r.ScoreKind, r.DocumentPath, r.ChunkIndex, r.ChunkContent)
	}

	return mcplib.NewToolResultText(output), nil
}

func (s *Server) handleListSources(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	docs, err := s.store.ListDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing documents: %w", err)
	}

	if len(docs) == 0 {
		return mcplib.NewToolResultText("No documents indexed."), nil
	}

	output := fmt.Sprintf("Indexed documents (%d):\n", len(docs))
	for _, doc := range docs {
		output += fmt.Sprintf("  %s (indexed: %s)\n", doc.Path, doc.IndexedAt.Format("2006-01-02 15:04:05"))
	}

	return mcplib.NewToolResultText(output), nil
}

func (s *Server) handleIndexStatus(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	docCount, chunkCount, err := s.store.Stats(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting stats: %w", err)
	}

	var dbSize int64
	if info, err := os.Stat(s.cfg.DBPath); err == nil {
		dbSize = info.Size()
	}

	output := fmt.Sprintf(
		"Index Status:\n  Documents: %d\n  Chunks: %d\n  DB Size: %s\n  Watch Dir: %s\n  Model: %s",
		docCount, chunkCount, formatBytes(dbSize), s.cfg.WatchDir, s.cfg.EmbedModel,
	)

	return mcplib.NewToolResultText(output), nil
}

func (s *Server) cachedEmbed(ctx context.Context, text string) ([]float32, error) {
	s.embCacheMu.Lock()
	if s.embCache == nil {
		s.embCache = newEmbeddingLRU(embCacheMaxSize)
	}
	if s.embFlights == nil {
		s.embFlights = make(map[string]*embeddingFlight)
	}
	if vec, ok := s.embCache.Get(text); ok {
		s.embCacheMu.Unlock()
		return vec, nil
	}
	if flight, ok := s.embFlights[text]; ok {
		s.embCacheMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			return flight.vec, flight.err
		}
	}
	flight := &embeddingFlight{done: make(chan struct{})}
	s.embFlights[text] = flight
	s.embCacheMu.Unlock()

	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		s.embCacheMu.Lock()
		delete(s.embFlights, text)
		flight.err = err
		close(flight.done)
		s.embCacheMu.Unlock()
		return nil, err
	}
	vec = index.NormalizeFloat32(vec)

	s.embCacheMu.Lock()
	s.embCache.Put(text, vec)
	delete(s.embFlights, text)
	flight.vec = vec
	flight.err = nil
	close(flight.done)
	s.embCacheMu.Unlock()

	return vec, nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
