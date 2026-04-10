package mcp

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

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

// maxQueryLength is the maximum number of characters accepted in a search query.
// Queries beyond this length are truncated before embedding to avoid sending
// unnecessarily large payloads to the embedding backend.
const maxQueryLength = 4000

func (s *Server) handleSearch(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := request.GetArguments()

	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len([]rune(query)) > maxQueryLength {
		query = string([]rune(query)[:maxQueryLength])
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

	startedAt := time.Now()
	log.Printf("MCP search request: query=%q limit=%d threshold=%.4f path=%q", summarizeLogText(query, 120), limit, threshold, pathPrefix)

	queryEmbedding, err := s.cachedEmbed(ctx, query)
	if err != nil {
		log.Printf("MCP search error: query=%q stage=embed path=%q err=%v duration=%s", summarizeLogText(query, 120), pathPrefix, err, time.Since(startedAt).Round(time.Millisecond))
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	results, err := s.store.Search(ctx, query, queryEmbedding, limit, pathPrefix)
	if err != nil {
		log.Printf("MCP search error: query=%q stage=search path=%q err=%v duration=%s", summarizeLogText(query, 120), pathPrefix, err, time.Since(startedAt).Round(time.Millisecond))
		return nil, fmt.Errorf("searching: %w", err)
	}

	var filtered []index.SearchResult
	for _, r := range results {
		if r.Score >= threshold {
			filtered = append(filtered, r)
		}
	}

	log.Printf(
		"MCP search result: query=%q path=%q raw_hits=%d returned=%d duration=%s spotlight=%s",
		summarizeLogText(query, 120),
		pathPrefix,
		len(results),
		len(filtered),
		time.Since(startedAt).Round(time.Millisecond),
		formatSearchSpotlights(filtered, 3),
	)

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
		log.Printf("MCP list_sources error: %v", err)
		return nil, fmt.Errorf("listing documents: %w", err)
	}

	log.Printf("MCP list_sources: count=%d spotlight=%s", len(docs), formatDocumentSpotlights(docs, 5))

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
		log.Printf("MCP index_status error: %v", err)
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

	log.Printf(
		"MCP index_status: documents=%d chunks=%d db_size=%s watch_dir=%q model=%q",
		docCount,
		chunkCount,
		formatBytes(dbSize),
		s.cfg.WatchDir,
		s.cfg.EmbedModel,
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
		flight.waiters++
		s.embCacheMu.Unlock()
		return s.waitForEmbeddingFlight(ctx, text, flight)
	}
	flight := &embeddingFlight{
		done:    make(chan struct{}),
		waiters: 1,
	}
	s.embFlights[text] = flight
	s.embCacheMu.Unlock()

	go s.runEmbeddingFlight(context.Background(), text, flight)

	return s.waitForEmbeddingFlight(ctx, text, flight)
}

func (s *Server) waitForEmbeddingFlight(ctx context.Context, text string, flight *embeddingFlight) ([]float32, error) {
	select {
	case <-ctx.Done():
		s.releaseEmbeddingFlight(text, flight)
		return nil, ctx.Err()
	case <-flight.done:
		s.releaseEmbeddingFlight(text, flight)
		return flight.vec, flight.err
	}
}

func (s *Server) runEmbeddingFlight(ctx context.Context, text string, flight *embeddingFlight) {
	vec, err := s.embedder.Embed(ctx, text)
	if err == nil {
		vec = index.NormalizeFloat32(vec)
	}

	s.embCacheMu.Lock()
	defer s.embCacheMu.Unlock()

	if err == nil {
		if s.embCache == nil {
			s.embCache = newEmbeddingLRU(embCacheMaxSize)
		}
		s.embCache.Put(text, vec)
	}
	delete(s.embFlights, text)
	flight.vec = vec
	flight.err = err
	close(flight.done)
}

func (s *Server) releaseEmbeddingFlight(_ string, flight *embeddingFlight) {
	s.embCacheMu.Lock()
	defer s.embCacheMu.Unlock()

	if flight.waiters > 0 {
		flight.waiters--
	}
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

func summarizeLogText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func formatSearchSpotlights(results []index.SearchResult, limit int) string {
	if len(results) == 0 {
		return "none"
	}
	if limit <= 0 || limit > len(results) {
		limit = len(results)
	}

	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		r := results[i]
		parts = append(parts, fmt.Sprintf(
			"%s#%d score=%.4f %s snippet=%q",
			r.DocumentPath,
			r.ChunkIndex,
			r.Score,
			r.ScoreKind,
			summarizeLogText(r.ChunkContent, 72),
		))
	}
	return strings.Join(parts, " | ")
}

func formatDocumentSpotlights(docs []index.Document, limit int) string {
	if len(docs) == 0 {
		return "none"
	}
	if limit <= 0 || limit > len(docs) {
		limit = len(docs)
	}

	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, docs[i].Path)
	}
	return strings.Join(parts, ", ")
}
