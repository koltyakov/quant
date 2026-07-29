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
		mcplib.WithTitleAnnotation("Search Index"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(true),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithString("query",
			mcplib.Required(),
			mcplib.Description("Search query text"),
			mcplib.MinLength(1),
			mcplib.MaxLength(maxQueryLength),
		),
		mcplib.WithInteger("limit",
			mcplib.Description("Maximum number of results (default: 5)"),
			mcplib.Min(1),
			mcplib.Max(maxSearchLimit),
		),
		mcplib.WithNumber("threshold",
			mcplib.Description("Minimum result score (0-1 normalized scale, default: 0)"),
			mcplib.Min(0.0),
			mcplib.Max(1.0),
		),
		mcplib.WithString("path",
			mcplib.Description("Filter results to documents whose path starts with this prefix"),
		),
		mcplib.WithString("file_type",
			mcplib.Description("Filter by file type (e.g. pdf, go, python, markdown)"),
		),
		mcplib.WithString("language",
			mcplib.Description("Filter by programming language (e.g. go, python, javascript)"),
		),
		mcplib.WithString("collection",
			mcplib.Description("Filter by exact collection name"),
			mcplib.MaxLength(256),
		),
	), s.handleSearch)

	s.mcp.AddTool(mcplib.NewTool("list_sources",
		mcplib.WithDescription("List indexed documents"),
		mcplib.WithTitleAnnotation("List Sources"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithInteger("limit",
			mcplib.Description("Maximum number of documents to return (default: 100)"),
			mcplib.Min(1),
			mcplib.Max(maxSourcesLimit),
		),
	), s.handleListSources)

	s.mcp.AddTool(mcplib.NewTool("index_status",
		mcplib.WithDescription("Get index statistics: total docs, chunks, DB size"),
		mcplib.WithTitleAnnotation("Index Status"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
	), s.handleIndexStatus)

	s.mcp.AddTool(mcplib.NewTool("find_similar",
		mcplib.WithDescription("Find chunks similar to a given chunk by its ID. Useful for discovering related code or content across the index."),
		mcplib.WithTitleAnnotation("Find Similar Chunks"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithInteger("chunk_id",
			mcplib.Required(),
			mcplib.Description("The chunk ID to find similar chunks for (from search results)"),
			mcplib.Min(1),
		),
		mcplib.WithInteger("limit",
			mcplib.Description("Maximum number of results (default: 5)"),
			mcplib.Min(1),
			mcplib.Max(maxSearchLimit),
		),
	), s.handleFindSimilar)

	s.mcp.AddTool(mcplib.NewTool("get_context",
		mcplib.WithDescription("Get a chunk and its ordered neighboring chunks from the same document"),
		mcplib.WithTitleAnnotation("Get Chunk Context"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithInteger("chunk_id",
			mcplib.Required(),
			mcplib.Description("The chunk ID from a previous search result"),
			mcplib.Min(1),
		),
		mcplib.WithInteger("before",
			mcplib.Description("Number of preceding chunks to return (default: 1)"),
			mcplib.Min(0),
			mcplib.Max(maxContextNeighbors),
		),
		mcplib.WithInteger("after",
			mcplib.Description("Number of following chunks to return (default: 1)"),
			mcplib.Min(0),
			mcplib.Max(maxContextNeighbors),
		),
	), s.handleGetContext)

	s.mcp.AddTool(mcplib.NewTool("list_collections",
		mcplib.WithDescription("List all named collections in the index with their document and chunk counts"),
		mcplib.WithTitleAnnotation("List Collections"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
	), s.handleListCollections)

	s.mcp.AddTool(mcplib.NewTool("delete_collection",
		mcplib.WithDescription("Delete all documents and chunks in a named collection"),
		mcplib.WithTitleAnnotation("Delete Collection"),
		mcplib.WithReadOnlyHintAnnotation(false),
		mcplib.WithDestructiveHintAnnotation(true),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithString("collection",
			mcplib.Required(),
			mcplib.Description("Name of the collection to delete"),
			mcplib.MinLength(1),
		),
	), s.handleDeleteCollection)

	s.mcp.AddTool(mcplib.NewTool("drill_down",
		mcplib.WithDescription("Drill into a topic by finding related chunks that expand on a seed chunk from a previous search result"),
		mcplib.WithTitleAnnotation("Drill Down"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(false),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithInteger("chunk_id",
			mcplib.Required(),
			mcplib.Description("The chunk ID to use as a seed for drilling deeper"),
			mcplib.Min(1),
		),
		mcplib.WithInteger("limit",
			mcplib.Description("Maximum number of results (default: 10)"),
			mcplib.Min(1),
			mcplib.Max(maxSearchLimit),
		),
	), s.handleDrillDown)

	s.mcp.AddTool(mcplib.NewTool("summarize_matches",
		mcplib.WithDescription("Return a concise, non-exhaustive overview of the top matching chunks and source documents for a query"),
		mcplib.WithTitleAnnotation("Summarize Matches"),
		mcplib.WithReadOnlyHintAnnotation(true),
		mcplib.WithDestructiveHintAnnotation(false),
		mcplib.WithIdempotentHintAnnotation(true),
		mcplib.WithOpenWorldHintAnnotation(true),
		mcplib.WithSchemaAdditionalProperties(false),
		mcplib.WithString("query",
			mcplib.Required(),
			mcplib.Description("The topic or query to summarize across the index"),
			mcplib.MinLength(1),
			mcplib.MaxLength(maxQueryLength),
		),
		mcplib.WithInteger("limit",
			mcplib.Description("Maximum number of source documents to consider (default: 20)"),
			mcplib.Min(1),
			mcplib.Max(maxSearchLimit),
		),
	), s.handleSummarizeMatches)
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
	maxContextNeighbors   = 5
	maxResultSnippetRunes = 1200
	maxSearchOutputRunes  = 12000
)

type searchToolResponse struct {
	Query           string                `json:"query"`
	PathPrefix      string                `json:"path_prefix,omitempty"`
	FileType        string                `json:"file_type,omitempty"`
	Language        string                `json:"language,omitempty"`
	Collection      string                `json:"collection,omitempty"`
	Limit           int                   `json:"limit"`
	Threshold       float32               `json:"threshold"`
	EmbeddingStatus string                `json:"embedding_status"`
	Note            string                `json:"note,omitempty"`
	Results         []searchToolResultRow `json:"results"`
}

type searchToolResultRow struct {
	ChunkID       int64   `json:"chunk_id"`
	Path          string  `json:"path"`
	ChunkIndex    int     `json:"chunk_index"`
	Score         float32 `json:"score"`
	ScoreKind     string  `json:"score_kind"`
	ChunkContent  string  `json:"chunk_content"`
	ParentID      *int64  `json:"parent_id,omitempty"`
	Depth         int     `json:"depth"`
	SectionTitle  string  `json:"section_title,omitempty"`
	ParentContext string  `json:"parent_context,omitempty"`
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
	Documents       int                   `json:"documents"`
	Chunks          int                   `json:"chunks"`
	DBSizeBytes     int64                 `json:"db_size_bytes"`
	DBSize          string                `json:"db_size"`
	WatchDir        string                `json:"watch_dir"`
	Model           string                `json:"model"`
	EmbeddingStatus string                `json:"embedding_status"`
	FTS             *index.FTSDiagnostics `json:"fts,omitempty"`
	State           string                `json:"state,omitempty"`
	StateMessage    string                `json:"state_message,omitempty"`
	StateUpdatedAt  time.Time             `json:"state_updated_at"`
	StateServing    bool                  `json:"state_serving"`
	StateFresh      bool                  `json:"state_fresh"`
}

type embeddingStatusProvider interface {
	EmbeddingStatus(ctx context.Context) (string, error)
}

type findSimilarToolResponse struct {
	ChunkID int64                 `json:"chunk_id"`
	Limit   int                   `json:"limit"`
	Source  searchToolResultRow   `json:"source"`
	Results []searchToolResultRow `json:"results"`
}

type getContextToolResponse struct {
	TargetChunkID int64                  `json:"target_chunk_id"`
	Before        int                    `json:"before"`
	After         int                    `json:"after"`
	Chunks        []getContextToolResult `json:"chunks"`
}

type getContextToolResult struct {
	ChunkID      int64  `json:"chunk_id"`
	Path         string `json:"path"`
	ChunkIndex   int    `json:"chunk_index"`
	ChunkContent string `json:"chunk_content"`
	IsTarget     bool   `json:"is_target"`
	ParentID     *int64 `json:"parent_id,omitempty"`
	Depth        int    `json:"depth"`
	SectionTitle string `json:"section_title,omitempty"`
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

	var filter index.SearchFilter
	fileType := ""
	if v, ok := args["file_type"].(string); ok {
		fileType = index.CanonicalFileType(v)
		if fileType == "" && strings.TrimSpace(v) != "" {
			return nil, fmt.Errorf("unrecognized file_type %q", v)
		}
		if fileType != "" {
			filter.FileTypes = []string{fileType}
		}
	}
	language := ""
	if v, ok := args["language"].(string); ok {
		language = strings.ToLower(strings.TrimSpace(v))
		if language != "" {
			filter.Languages = []string{language}
		}
	}
	collection := ""
	if v, ok := args["collection"].(string); ok {
		collection = strings.TrimSpace(v)
		filter.Collection = collection
	}

	startedAt := time.Now()
	logx.Info("MCP search request", "query", summarizeLogText(query, 120), "limit", limit, "threshold", threshold, "path", pathPrefix, "filter", filter)

	queryEmbedding, embedErr := s.cachedEmbed(ctx, query)
	if embedErr != nil {
		logx.Warn("MCP search embedding failed; falling back to keyword-only", "query", summarizeLogText(query, 120), "err", embedErr, "duration", time.Since(startedAt).Round(time.Millisecond))
	}

	var results []index.SearchResult
	var err error
	if len(filter.FileTypes) > 0 || len(filter.Languages) > 0 || len(filter.Tags) > 0 || filter.Collection != "" {
		results, err = s.store.SearchFiltered(ctx, query, queryEmbedding, limit, pathPrefix, filter)
	} else {
		results, err = s.store.Search(ctx, query, queryEmbedding, limit, pathPrefix)
	}
	if err != nil {
		logx.Error("MCP search error", "query", summarizeLogText(query, 120), "stage", "search", "path", pathPrefix, "err", err, "duration", time.Since(startedAt).Round(time.Millisecond))
		return nil, fmt.Errorf("searching: %w", err)
	}

	if enricher, ok := s.store.(interface {
		EnrichWithParentContext(context.Context, []index.SearchResult) []index.SearchResult
	}); ok && len(results) > 0 {
		results = enricher.EnrichWithParentContext(ctx, results)
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
			FileType:        fileType,
			Language:        language,
			Collection:      collection,
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
		FileType:        fileType,
		Language:        language,
		Collection:      collection,
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "Indexed documents (%d total", total)
	if len(docs) != total {
		fmt.Fprintf(&sb, ", showing first %d", len(docs))
	}
	sb.WriteString("):\n")
	for _, doc := range docs {
		fmt.Fprintf(&sb, "  %s (indexed: %s)\n", doc.Path, doc.IndexedAt.Format("2006-01-02 15:04:05"))
	}
	if len(docs) != total {
		fmt.Fprintf(&sb, "  ... and %d more\n", total-len(docs))
	}
	output := sb.String()

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
	embedStatus := s.embeddingStatus(ctx)
	structured := indexStatusToolResponse{
		Documents:       docCount,
		Chunks:          chunkCount,
		DBSizeBytes:     dbSize,
		DBSize:          formatBytes(dbSize),
		WatchDir:        s.cfg.WatchDir,
		Model:           s.cfg.EmbedModel,
		EmbeddingStatus: embedStatus,
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
		structured.StateServing = snapshot.Servable()
		structured.StateFresh = snapshot.Fresh()
	}

	output := fmt.Sprintf(
		"Index Status:\n  Documents: %d\n  Chunks: %d\n  DB Size: %s\n  Watch Dir: %s\n  Model: %s\n  Embedding: %s",
		docCount, chunkCount, formatBytes(dbSize), s.cfg.WatchDir, s.cfg.EmbedModel, embedStatus,
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
		output += fmt.Sprintf("\n  Serving Queries: %t", structured.StateServing)
		output += fmt.Sprintf("\n  Index Fresh: %t", structured.StateFresh)
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

func (s *Server) handleGetContext(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	args := request.GetArguments()
	chunkID, ok := args["chunk_id"].(float64)
	if !ok || math.IsNaN(chunkID) || math.IsInf(chunkID, 0) || chunkID <= 0 || chunkID != float64(int64(chunkID)) {
		return nil, fmt.Errorf("chunk_id must be a positive integer")
	}
	before, err := contextNeighborCount(args, "before")
	if err != nil {
		return nil, err
	}
	after, err := contextNeighborCount(args, "after")
	if err != nil {
		return nil, err
	}

	chunks, err := s.store.GetChunkWindow(ctx, int64(chunkID), before, after)
	if err != nil {
		return nil, fmt.Errorf("getting context for chunk %d: %w", int64(chunkID), err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("chunk %d not found", int64(chunkID))
	}

	rows := make([]getContextToolResult, 0, len(chunks))
	var output strings.Builder
	fmt.Fprintf(&output, "Context for chunk %d (%s):\n\n", int64(chunkID), chunks[0].DocumentPath)
	for _, chunk := range chunks {
		isTarget := chunk.ChunkID == int64(chunkID)
		rows = append(rows, getContextToolResult{
			ChunkID:      chunk.ChunkID,
			Path:         chunk.DocumentPath,
			ChunkIndex:   chunk.ChunkIndex,
			ChunkContent: chunk.ChunkContent,
			IsTarget:     isTarget,
			ParentID:     chunk.ParentID,
			Depth:        chunk.Depth,
			SectionTitle: chunk.SectionTitle,
		})
		marker := "neighbor"
		if isTarget {
			marker = "target"
		}
		fmt.Fprintf(&output, "[%s chunk %d, index %d]\n%s\n\n", marker, chunk.ChunkID, chunk.ChunkIndex, chunk.ChunkContent)
	}

	structured := getContextToolResponse{
		TargetChunkID: int64(chunkID),
		Before:        before,
		After:         after,
		Chunks:        rows,
	}
	return mcplib.NewToolResultStructured(structured, truncateRunes(output.String(), maxSearchOutputRunes)), nil
}

func contextNeighborCount(args map[string]any, name string) (int, error) {
	v, ok := args[name]
	if !ok {
		return 1, nil
	}
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > maxContextNeighbors || n != float64(int(n)) {
		return 0, fmt.Errorf("%s must be an integer between 0 and %d", name, maxContextNeighbors)
	}
	return int(n), nil
}

type collectionInfo struct {
	Name      string `json:"name"`
	Documents int    `json:"documents"`
	Chunks    int    `json:"chunks"`
}

type listCollectionsResponse struct {
	Collections []collectionInfo `json:"collections"`
}

func (s *Server) handleListCollections(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	collections, err := s.store.ListCollections(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing collections: %w", err)
	}

	infos := make([]collectionInfo, 0, len(collections))
	var output strings.Builder
	output.WriteString("Collections:\n")

	for _, name := range collections {
		docs, chunks, err := s.store.CollectionStats(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("getting stats for collection %q: %w", name, err)
		}
		infos = append(infos, collectionInfo{Name: name, Documents: docs, Chunks: chunks})
		fmt.Fprintf(&output, "  %s (%d docs, %d chunks)\n", name, docs, chunks)
	}

	if len(collections) == 0 {
		output.WriteString("  (none)\n")
	}

	structured := listCollectionsResponse{Collections: infos}
	return mcplib.NewToolResultStructured(structured, output.String()), nil
}

func (s *Server) handleDeleteCollection(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if err := s.acquireToolSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseToolSlot()

	args := request.GetArguments()
	collection, ok := args["collection"].(string)
	if !ok || collection == "" {
		return nil, fmt.Errorf("collection is required")
	}

	if err := s.store.DeleteCollection(ctx, collection); err != nil {
		return nil, fmt.Errorf("deleting collection: %w", err)
	}

	output := fmt.Sprintf("Collection %q deleted.", collection)
	return mcplib.NewToolResultStructured(map[string]string{"collection": collection, "status": "deleted"}, output), nil
}

func embeddingStatus(err error) string {
	if err != nil {
		return "keyword_only"
	}
	return "hybrid"
}

type drillDownResponse struct {
	SeedChunkID int64                 `json:"seed_chunk_id"`
	Limit       int                   `json:"limit"`
	Results     []searchToolResultRow `json:"results"`
}

func (s *Server) handleDrillDown(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
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

	limit := 10
	if v, ok := args["limit"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) || v != float64(int(v)) {
			return nil, fmt.Errorf("limit must be an integer between 1 and %d", maxSearchLimit)
		}
		limit = int(v)
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}

	source, err := s.store.GetChunkByID(ctx, int64(chunkID))
	if err != nil {
		return nil, fmt.Errorf("chunk %d not found: %w", int64(chunkID), err)
	}

	candidateLimit := min(maxSearchLimit, limit*3)
	results, err := s.store.FindSimilar(ctx, int64(chunkID), candidateLimit)
	if err != nil {
		return nil, fmt.Errorf("finding related chunks: %w", err)
	}

	diverse := diversifySearchResults(source.DocumentPath, results, limit)

	output := fmt.Sprintf("Drill-down from chunk %d (%s):\n\n", int64(chunkID), source.DocumentPath)
	output += formatSearchResults(diverse)

	structured := drillDownResponse{
		SeedChunkID: int64(chunkID),
		Limit:       limit,
		Results:     searchRows(diverse),
	}
	return mcplib.NewToolResultStructured(structured, output), nil
}

func diversifySearchResults(seedPath string, results []index.SearchResult, limit int) []index.SearchResult {
	if limit <= 0 || len(results) == 0 {
		return nil
	}
	limit = min(limit, len(results))
	selected := make([]index.SearchResult, 0, limit)
	selectedIDs := make(map[int64]bool, limit)
	seenPaths := map[string]bool{seedPath: true}

	for _, result := range results {
		if len(selected) >= limit {
			break
		}
		if seenPaths[result.DocumentPath] {
			continue
		}
		selected = append(selected, result)
		selectedIDs[result.ChunkID] = true
		seenPaths[result.DocumentPath] = true
	}
	for _, result := range results {
		if len(selected) >= limit {
			break
		}
		if selectedIDs[result.ChunkID] {
			continue
		}
		selected = append(selected, result)
		selectedIDs[result.ChunkID] = true
	}
	return selected
}

type summarizeMatchesResponse struct {
	Query           string   `json:"query"`
	Limit           int      `json:"limit"`
	MatchCount      int      `json:"match_count"`
	DocumentCount   int      `json:"document_count"`
	Documents       []string `json:"documents"`
	EmbeddingStatus string   `json:"embedding_status"`
	Note            string   `json:"note,omitempty"`
	Exhaustive      bool     `json:"exhaustive"`
	Overview        string   `json:"overview"`
}

func (s *Server) handleSummarizeMatches(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
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

	limit := 20
	if v, ok := args["limit"].(float64); ok {
		if math.IsNaN(v) || math.IsInf(v, 0) || v != float64(int(v)) {
			return nil, fmt.Errorf("limit must be an integer between 1 and %d", maxSearchLimit)
		}
		limit = int(v)
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}

	queryEmbedding, embedErr := s.cachedEmbed(ctx, query)
	if embedErr != nil {
		logx.Warn("summarize_matches embedding failed", "err", embedErr)
	}

	results, err := s.store.Search(ctx, query, queryEmbedding, limit, "")
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}

	seen := make(map[string]bool)
	var uniqueDocs []string
	for _, r := range results {
		if !seen[r.DocumentPath] {
			seen[r.DocumentPath] = true
			uniqueDocs = append(uniqueDocs, r.DocumentPath)
		}
	}

	var overview strings.Builder
	fmt.Fprintf(&overview, "Top-match overview: %d chunks across %d documents for query %q (limit %d; not exhaustive)\n\n",
		len(results), len(uniqueDocs), query, limit)
	if embedErr != nil {
		overview.WriteString("[Note: embedding backend unavailable; overview is based on keyword-only results]\n\n")
	}

	if len(uniqueDocs) > 0 {
		overview.WriteString("Matching documents:\n")
		for _, doc := range uniqueDocs {
			fmt.Fprintf(&overview, "  - %s\n", doc)
		}
		overview.WriteString("\n")
	}

	if len(results) > 0 {
		overview.WriteString("Key excerpts:\n")
		for i, r := range results {
			if i >= 5 {
				break
			}
			snippet := strings.TrimSpace(r.ChunkContent)
			if len([]rune(snippet)) > 200 {
				snippet = string([]rune(snippet)[:200]) + "..."
			}
			fmt.Fprintf(&overview, "  [%s] %s\n", r.DocumentPath, snippet)
		}
	}

	structured := summarizeMatchesResponse{
		Query:           query,
		Limit:           limit,
		MatchCount:      len(results),
		DocumentCount:   len(uniqueDocs),
		Documents:       uniqueDocs,
		EmbeddingStatus: embeddingStatus(embedErr),
		Note:            embeddingNote(embedErr),
		Exhaustive:      false,
		Overview:        overview.String(),
	}
	return mcplib.NewToolResultStructured(structured, overview.String()), nil
}

func embeddingNote(err error) string {
	if err == nil {
		return ""
	}
	return "embedding backend unavailable; showing keyword-only results"
}

func (s *Server) cachedEmbed(ctx context.Context, text string) ([]float32, error) {
	if s.embedder == nil {
		if fallback, ok := s.store.(interface {
			Embed(context.Context, string) ([]float32, error)
		}); ok {
			return fallback.Embed(ctx, text)
		}
		return nil, fmt.Errorf("embedding backend unavailable")
	}
	return s.embedder.Embed(ctx, text)
}

func (s *Server) embeddingStatus(ctx context.Context) string {
	if provider, ok := s.store.(embeddingStatusProvider); ok {
		status, err := provider.EmbeddingStatus(ctx)
		if err == nil && strings.TrimSpace(status) != "" {
			return status
		}
	}
	if s.embedder == nil {
		return "unavailable (keyword-only mode) — start Ollama with: ollama serve"
	}
	return "available"
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
