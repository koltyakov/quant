package mcp

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
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
			mcplib.Description("Minimum result score (0-1 normalized scale, default: 0)"),
		),
		mcplib.WithString("path",
			mcplib.Description("Filter results to documents whose path starts with this prefix"),
		),
	), s.handleSearch)

	s.mcp.AddTool(mcplib.NewTool("list_sources",
		mcplib.WithDescription("List indexed documents"),
		mcplib.WithNumber("limit",
			mcplib.Description("Maximum number of documents to return (default: 100)"),
		),
	), s.handleListSources)

	s.mcp.AddTool(mcplib.NewTool("index_status",
		mcplib.WithDescription("Get index statistics: total docs, chunks, DB size"),
	), s.handleIndexStatus)

	s.mcp.AddTool(mcplib.NewTool("find_similar",
		mcplib.WithDescription("Find chunks similar to a given chunk by its ID. Useful for discovering related code or content across the index."),
		mcplib.WithNumber("chunk_id",
			mcplib.Required(),
			mcplib.Description("The chunk ID to find similar chunks for (from search results)"),
		),
		mcplib.WithNumber("limit",
			mcplib.Description("Maximum number of results (default: 5)"),
		),
	), s.handleFindSimilar)
}

// maxQueryLength is the maximum number of characters accepted in a search query.
// Queries beyond this length are truncated before embedding to avoid sending
// unnecessarily large payloads to the embedding backend.
const (
	maxQueryLength        = 4000
	defaultSearchLimit    = 5
	maxSearchLimit        = 50
	defaultSourcesLimit   = 100
	maxSourcesLimit       = 500
	maxResultSnippetRunes = 1200
	maxSearchOutputRunes  = 12000
)

type searchToolResponse struct {
	Query           string                `json:"query"`
	PathPrefix      string                `json:"path_prefix,omitempty"`
	Limit           int                   `json:"limit"`
	Threshold       float32               `json:"threshold"`
	EmbeddingStatus string                `json:"embedding_status"`
	Note            string                `json:"note,omitempty"`
	Results         []searchToolResultRow `json:"results"`
}

type searchToolResultRow struct {
	ChunkID      int64   `json:"chunk_id"`
	Path         string  `json:"path"`
	ChunkIndex   int     `json:"chunk_index"`
	Score        float32 `json:"score"`
	ScoreKind    string  `json:"score_kind"`
	ChunkContent string  `json:"chunk_content"`
}

type listSourcesToolResponse struct {
	Total   int                    `json:"total"`
	Limit   int                    `json:"limit"`
	Shown   int                    `json:"shown"`
	Sources []listSourcesResultRow `json:"sources"`
}

type listSourcesResultRow struct {
	Path      string    `json:"path"`
	IndexedAt time.Time `json:"indexed_at"`
}

type indexStatusToolResponse struct {
	Documents      int                   `json:"documents"`
	Chunks         int                   `json:"chunks"`
	DBSizeBytes    int64                 `json:"db_size_bytes"`
	DBSize         string                `json:"db_size"`
	WatchDir       string                `json:"watch_dir"`
	Model          string                `json:"model"`
	FTS            *index.FTSDiagnostics `json:"fts,omitempty"`
	State          string                `json:"state,omitempty"`
	StateMessage   string                `json:"state_message,omitempty"`
	StateUpdatedAt time.Time             `json:"state_updated_at,omitempty"`
}

type findSimilarToolResponse struct {
	ChunkID int64                 `json:"chunk_id"`
	Limit   int                   `json:"limit"`
	Source  searchToolResultRow   `json:"source"`
	Results []searchToolResultRow `json:"results"`
}

func (s *Server) handleSearch(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

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

	limit := defaultSearchLimit
	if v, ok := args["limit"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("limit must be a finite number between 1 and %d", maxSearchLimit)
		}
		limit = int(v)
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}

	threshold := float32(0)
	if v, ok := args["threshold"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("threshold must be a finite number")
		}
		threshold = float32(v)
	}

	pathPrefix := ""
	if v, ok := args["path"].(string); ok {
		normalizedPath, normErr := normalizeSearchPathPrefix(s.cfg.WatchDir, v)
		if normErr != nil {
			return nil, normErr
		}
		pathPrefix = normalizedPath
	}

	startedAt := time.Now()
	logx.Info("MCP search request", "query", summarizeLogText(query, 120), "limit", limit, "threshold", threshold, "path", pathPrefix)

	queryEmbedding, embedErr := s.cachedEmbed(ctx, query)
	if embedErr != nil {
		logx.Warn("MCP search embedding failed; falling back to keyword-only", "query", summarizeLogText(query, 120), "err", embedErr, "duration", time.Since(startedAt).Round(time.Millisecond))
	}

	results, err := s.store.Search(ctx, query, queryEmbedding, limit, pathPrefix)
	if err != nil {
		logx.Error("MCP search error", "query", summarizeLogText(query, 120), "stage", "search", "path", pathPrefix, "err", err, "duration", time.Since(startedAt).Round(time.Millisecond))
		return nil, fmt.Errorf("searching: %w", err)
	}

	var filtered []index.SearchResult
	for _, r := range results {
		if r.Score >= threshold {
			filtered = append(filtered, r)
		}
	}

	logx.Info("MCP search result", "query", summarizeLogText(query, 120), "path", pathPrefix, "raw_hits", len(results), "returned", len(filtered), "duration", time.Since(startedAt).Round(time.Millisecond), "spotlight", formatSearchSpotlights(filtered, 3))

	if len(filtered) == 0 {
		structured := searchToolResponse{
			Query:           query,
			PathPrefix:      pathPrefix,
			Limit:           limit,
			Threshold:       threshold,
			EmbeddingStatus: embeddingStatus(embedErr),
			Note:            embeddingNote(embedErr),
			Results:         nil,
		}
		return mcplib.NewToolResultStructured(structured, "No results found."), nil
	}

	output := formatSearchResults(filtered)
	structured := searchToolResponse{
		Query:           query,
		PathPrefix:      pathPrefix,
		Limit:           limit,
		Threshold:       threshold,
		EmbeddingStatus: embeddingStatus(embedErr),
		Note:            embeddingNote(embedErr),
		Results:         searchRows(filtered),
	}
	if embedErr != nil {
		output = "[Note: embedding backend unavailable; showing keyword-only results]\n\n" + output
	}
	return mcplib.NewToolResultStructured(structured, output), nil
}

func normalizeSearchPathPrefix(watchDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	hasTrailingSep := strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, `\`)
	path := raw
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(watchDir, path)
		if err != nil {
			return "", fmt.Errorf("invalid search path %q: %w", raw, err)
		}
		path = rel
	}

	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." {
		return "", nil
	}
	if path == ".." || strings.HasPrefix(path, "../") {
		return "", fmt.Errorf("search path %q is outside watch dir", raw)
	}
	if hasTrailingSep && path != "" && !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path, nil
}

func (s *Server) handleListSources(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	args := request.GetArguments()
	limit := defaultSourcesLimit
	if v, ok := args["limit"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("limit must be a finite number between 1 and %d", maxSourcesLimit)
		}
		limit = int(v)
	}
	if limit < 1 || limit > maxSourcesLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxSourcesLimit)
	}

	docCount, _, err := s.store.Stats(ctx)
	if err != nil {
		logx.Error("MCP list_sources error", "err", err)
		return nil, fmt.Errorf("listing documents: %w", err)
	}

	docs, err := s.store.ListDocumentsLimit(ctx, limit)
	if err != nil {
		logx.Error("MCP list_sources error", "err", err)
		return nil, fmt.Errorf("listing documents: %w", err)
	}

	logx.Info("MCP list_sources", "count", docCount, "returned", len(docs), "spotlight", formatDocumentSpotlights(docs, 5))

	structured := listSourcesToolResponse{
		Total:   docCount,
		Limit:   limit,
		Shown:   len(docs),
		Sources: listSourceRows(docs),
	}

	if docCount == 0 {
		return mcplib.NewToolResultStructured(structured, "No documents indexed."), nil
	}

	total := docCount

	output := fmt.Sprintf("Indexed documents (%d total", total)
	if len(docs) != total {
		output += fmt.Sprintf(", showing first %d", len(docs))
	}
	output += "):\n"
	for _, doc := range docs {
		output += fmt.Sprintf("  %s (indexed: %s)\n", doc.Path, doc.IndexedAt.Format("2006-01-02 15:04:05"))
	}
	if len(docs) != total {
		output += fmt.Sprintf("  ... and %d more\n", total-len(docs))
	}

	return mcplib.NewToolResultStructured(structured, output), nil
}

func (s *Server) handleIndexStatus(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	docCount, chunkCount, err := s.store.Stats(ctx)
	if err != nil {
		logx.Error("MCP index_status error", "err", err)
		return nil, fmt.Errorf("getting stats: %w", err)
	}

	dbSize := sqliteDiskUsage(s.cfg.DBPath)
	structured := indexStatusToolResponse{
		Documents:   docCount,
		Chunks:      chunkCount,
		DBSizeBytes: dbSize,
		DBSize:      formatBytes(dbSize),
		WatchDir:    s.cfg.WatchDir,
		Model:       s.cfg.EmbedModel,
	}
	if provider, ok := s.store.(index.FTSDiagnosticsProvider); ok {
		diag, diagErr := provider.FTSDiagnostics(ctx)
		if diagErr != nil {
			logx.Warn("MCP index_status fts diagnostics error", "err", diagErr)
		} else {
			structured.FTS = &diag
		}
	}
	if s.state != nil {
		snapshot := s.state.Snapshot()
		structured.State = string(snapshot.State)
		structured.StateMessage = snapshot.Message
		structured.StateUpdatedAt = snapshot.UpdatedAt
	}

	output := fmt.Sprintf(
		"Index Status:\n  Documents: %d\n  Chunks: %d\n  DB Size: %s\n  Watch Dir: %s\n  Model: %s",
		docCount, chunkCount, formatBytes(dbSize), s.cfg.WatchDir, s.cfg.EmbedModel,
	)
	if structured.FTS != nil {
		output += fmt.Sprintf(
			"\n  FTS: empty=%t, logical_rows=%d, data_rows=%d, idx_rows=%d",
			structured.FTS.Empty, structured.FTS.LogicalRows, structured.FTS.DataRows, structured.FTS.IdxRows,
		)
	}
	if structured.State != "" {
		output += fmt.Sprintf("\n  State: %s", structured.State)
		if structured.StateMessage != "" {
			output += fmt.Sprintf("\n  State Detail: %s", structured.StateMessage)
		}
	}

	logx.Info("MCP index_status", "documents", docCount, "chunks", chunkCount, "db_size", formatBytes(dbSize), "watch_dir", s.cfg.WatchDir, "model", s.cfg.EmbedModel, "state", structured.State)

	return mcplib.NewToolResultStructured(structured, output), nil
}

func (s *Server) handleFindSimilar(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	args := request.GetArguments()

	chunkID, ok := args["chunk_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("chunk_id is required")
	}
	if math.IsNaN(chunkID) || math.IsInf(chunkID, 0) || chunkID <= 0 || chunkID != float64(int64(chunkID)) {
		return nil, fmt.Errorf("chunk_id must be a positive integer")
	}

	limit := defaultSearchLimit
	if v, ok := args["limit"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("limit must be a finite number between 1 and %d", maxSearchLimit)
		}
		limit = int(v)
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}

	startedAt := time.Now()

	source, err := s.store.GetChunkByID(ctx, int64(chunkID))
	if err != nil {
		return nil, fmt.Errorf("chunk %d not found: %w", int64(chunkID), err)
	}

	results, err := s.store.FindSimilar(ctx, int64(chunkID), limit)
	if err != nil {
		return nil, fmt.Errorf("finding similar chunks: %w", err)
	}

	logx.Info("MCP find_similar", "chunk_id", int64(chunkID), "source", source.DocumentPath, "results", len(results), "duration", time.Since(startedAt).Round(time.Millisecond))

	if len(results) == 0 {
		structured := findSimilarToolResponse{
			ChunkID: int64(chunkID),
			Limit:   limit,
			Source:  searchRow(*source),
			Results: nil,
		}
		return mcplib.NewToolResultStructured(structured, "No similar chunks found."), nil
	}

	header := fmt.Sprintf("Source chunk %d from %s (chunk %d):\n%s\n\nSimilar chunks:\n",
		int64(chunkID), source.DocumentPath, source.ChunkIndex,
		summarizeLogText(source.ChunkContent, 200))

	output := header + formatSearchResults(results)
	structured := findSimilarToolResponse{
		ChunkID: int64(chunkID),
		Limit:   limit,
		Source:  searchRow(*source),
		Results: searchRows(results),
	}
	return mcplib.NewToolResultStructured(structured, output), nil
}

func embeddingStatus(err error) string {
	if err != nil {
		return "keyword_only"
	}
	return "hybrid"
}

func embeddingNote(err error) string {
	if err == nil {
		return ""
	}
	return "embedding backend unavailable; showing keyword-only results"
}

func searchRows(results []index.SearchResult) []searchToolResultRow {
	rows := make([]searchToolResultRow, 0, len(results))
	for _, result := range results {
		rows = append(rows, searchRow(result))
	}
	return rows
}

func searchRow(result index.SearchResult) searchToolResultRow {
	return searchToolResultRow{
		ChunkID:      result.ChunkID,
		Path:         result.DocumentPath,
		ChunkIndex:   result.ChunkIndex,
		Score:        result.Score,
		ScoreKind:    result.ScoreKind,
		ChunkContent: result.ChunkContent,
	}
}

func listSourceRows(docs []index.Document) []listSourcesResultRow {
	rows := make([]listSourcesResultRow, 0, len(docs))
	for _, doc := range docs {
		rows = append(rows, listSourcesResultRow{
			Path:      doc.Path,
			IndexedAt: doc.IndexedAt,
		})
	}
	return rows
}

func (s *Server) cachedEmbed(ctx context.Context, text string) ([]float32, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("embedding backend unavailable")
	}
	return s.embedder.Embed(ctx, text)
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

func sqliteDiskUsage(dbPath string) int64 {
	var total int64
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(path)
		if err == nil {
			total += info.Size()
		}
	}
	return total
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

func formatSearchResults(results []index.SearchResult) string {
	if len(results) == 0 {
		return "No results found."
	}

	var b strings.Builder
	remaining := maxSearchOutputRunes
	rendered := 0

	for i, r := range results {
		entry := renderSearchResultEntry(i+1, r, maxResultSnippetRunes)
		entryRunes := len([]rune(entry))
		if entryRunes > remaining {
			if rendered == 0 {
				entry = renderSearchResultEntry(i+1, r, entrySnippetBudget(r, remaining))
				entryRunes = len([]rune(entry))
				if entryRunes > remaining {
					entry = truncateRunes(entry, remaining)
					entryRunes = len([]rune(entry))
				}
				if entryRunes > 0 {
					b.WriteString(entry)
					remaining -= entryRunes
					rendered++
				}
			}
			break
		}

		b.WriteString(entry)
		remaining -= entryRunes
		rendered++
	}

	if omitted := len(results) - rendered; omitted > 0 && remaining > 0 {
		footer := fmt.Sprintf("[omitted %d additional result(s) to stay within the output budget]\n", omitted)
		if len([]rune(footer)) > remaining {
			footer = truncateRunes(footer, remaining)
		}
		b.WriteString(footer)
	}

	return b.String()
}

func renderSearchResultEntry(position int, result index.SearchResult, snippetLimit int) string {
	header := fmt.Sprintf(
		"--- Result %d (score: %.4f, kind: %s, chunk_id: %d) ---\nFile: %s (chunk %d)\n",
		position,
		result.Score,
		result.ScoreKind,
		result.ChunkID,
		result.DocumentPath,
		result.ChunkIndex,
	)

	content := strings.TrimSpace(result.ChunkContent)
	snippet, truncated := truncateRunesWithFlag(content, snippetLimit)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(snippet)
	if truncated {
		b.WriteString("\n[chunk content truncated]")
	}
	b.WriteString("\n\n")
	return b.String()
}

func entrySnippetBudget(result index.SearchResult, totalBudget int) int {
	header := fmt.Sprintf(
		"--- Result %d (score: %.4f, kind: %s, chunk_id: %d) ---\nFile: %s (chunk %d)\n",
		1,
		result.Score,
		result.ScoreKind,
		result.ChunkID,
		result.DocumentPath,
		result.ChunkIndex,
	)
	reserved := len([]rune(header)) + len([]rune("\n[chunk content truncated]\n\n"))
	if totalBudget <= reserved {
		return 0
	}
	return totalBudget - reserved
}

func truncateRunesWithFlag(text string, limit int) (string, bool) {
	if limit <= 0 {
		return "", strings.TrimSpace(text) != ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

func truncateRunes(text string, limit int) string {
	truncated, _ := truncateRunesWithFlag(text, limit)
	return truncated
}
