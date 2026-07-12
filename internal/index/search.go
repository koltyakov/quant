package index

import (
	"container/heap"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/logx"
)

func (s *Store) Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error) {
	return s.SearchFiltered(ctx, query, queryEmbedding, limit, pathPrefix, SearchFilter{})
}

func (s *Store) SearchFiltered(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string, filter SearchFilter) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	metadataWhere, metadataArgs := s.buildMetadataFilter(filter)

	andQuery, orQuery, nearQuery := buildFTSQueries(query)
	keywordCandidates := make(map[int]*searchCandidate)
	rankOffset := 0
	candidateLimit := searchCandidateLimit(limit)

	if andQuery != "" {
		collected, err := s.collectFTSCandidatesFiltered(ctx, andQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates, metadataWhere, metadataArgs)
		if err != nil {
			return nil, err
		}
		rankOffset += collected

		if len(keywordCandidates) >= candidateLimit {
			orQuery = ""
			nearQuery = ""
		}
	}

	if orQuery != "" && orQuery != andQuery {
		collected, err := s.collectFTSCandidatesFiltered(ctx, orQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates, metadataWhere, metadataArgs)
		if err != nil {
			return nil, err
		}
		rankOffset += collected
	}

	if nearQuery != "" {
		_, err := s.collectFTSCandidatesFiltered(ctx, nearQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates, metadataWhere, metadataArgs)
		if err != nil {
			return nil, err
		}
	}

	var docFilter map[string]float32
	if queryEmbedding != nil && pathPrefix == "" && metadataWhere == "" {
		docFilter = s.docEmbeds.topDocPaths(queryEmbedding, docFilterTopK)
	}

	var vectorOnlyCandidates map[int]*searchCandidate
	if queryEmbedding != nil {
		var err error
		vectorOnlyCandidates, err = s.collectVectorCandidates(ctx, queryEmbedding, limit, pathPrefix, keywordCandidates, docFilter, metadataWhere, metadataArgs)
		if err != nil {
			return nil, err
		}
	}

	weights := classifyQueryWeights(query, s.keywordWeightOverride, s.vectorWeightOverride)
	results := unifiedRRF(keywordCandidates, vectorOnlyCandidates, limit, pathQueryTokens(query), weights)

	s.hydrateResultContents(ctx, results)

	if s.reranker != nil {
		reranked, err := s.reranker.Rerank(ctx, query, queryEmbedding, results)
		if err == nil && len(reranked) > 0 {
			results = reranked
		}
	}

	return results, nil
}

func (s *Store) GetChunkByID(ctx context.Context, chunkID int64) (*SearchResult, error) {
	var content string
	var chunkIndex int
	var docPath string
	var parentID *int64
	var depth int
	var sectionTitle string
	err := s.db.QueryRowContext(ctx,
		`SELECT c.content, c.chunk_index, d.path, c.parent_id, c.depth, c.section_title
		 FROM chunks c
		 JOIN documents d ON c.document_id = d.id
		 WHERE c.id = ?`,
		chunkID,
	).Scan(&content, &chunkIndex, &docPath, &parentID, &depth, &sectionTitle)
	if err != nil {
		return nil, err
	}
	return &SearchResult{
		ChunkID:      chunkID,
		ChunkContent: content,
		ChunkIndex:   chunkIndex,
		DocumentPath: docPath,
		ParentID:     parentID,
		Depth:        depth,
		SectionTitle: sectionTitle,
	}, nil
}

// GetChunkWindow returns a target chunk and its ordered neighbors from the
// same document. Chunk IDs break ties to keep results deterministic if an
// imported index contains duplicate chunk indexes.
func (s *Store) GetChunkWindow(ctx context.Context, chunkID int64, before, after int) ([]SearchResult, error) {
	if before < 0 || after < 0 {
		return nil, fmt.Errorf("context window sizes must be non-negative")
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH target AS (
			SELECT document_id, chunk_index FROM chunks WHERE id = ?
		)
		SELECT c.id, c.content, c.chunk_index, d.path, c.parent_id, c.depth, c.section_title
		FROM target
		JOIN chunks c ON c.document_id = target.document_id
		JOIN documents d ON d.id = c.document_id
		WHERE c.chunk_index BETWEEN target.chunk_index - ? AND target.chunk_index + ?
		ORDER BY c.chunk_index, c.id`, chunkID, before, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	results := make([]SearchResult, 0, before+after+1)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(
			&result.ChunkID,
			&result.ChunkContent,
			&result.ChunkIndex,
			&result.DocumentPath,
			&result.ParentID,
			&result.Depth,
			&result.SectionTitle,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, sql.ErrNoRows
	}
	return results, nil
}

func (s *Store) FindSimilar(ctx context.Context, chunkID int64, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	var embeddingBytes []byte
	if err := s.db.QueryRowContext(ctx, `SELECT embedding FROM chunks WHERE id = ?`, chunkID).Scan(&embeddingBytes); err != nil {
		return nil, fmt.Errorf("loading chunk %d: %w", chunkID, err)
	}

	meta, err := s.embeddingMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.Dimensions == 0 {
		return nil, nil
	}

	vec := decodeEmbeddingForHNSW(embeddingBytes, meta.Dimensions)
	if len(vec) == 0 {
		return nil, nil
	}

	queryEmbed := NormalizeFloat32(vec)
	vectorOnly := make(map[int]*searchCandidate)

	if s.hnsw != nil && s.hnsw.ready.Load() {
		fetchK := limit + 1
		ids := s.hnsw.Search(queryEmbed, fetchK)
		filtered := make([]int, 0, len(ids))
		for _, id := range ids {
			if id != int(chunkID) {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) > 0 {
			s.loadHNSWChunkRows(ctx, filtered, queryEmbed, limit, nil, vectorOnly, nil)
		}
	}

	candidates := mergeCandidates(nil, vectorOnly)
	for i := range candidates {
		if candidates[i].result.ChunkID == chunkID {
			candidates = append(candidates[:i], candidates[i+1:]...)
			break
		}
	}

	results := make([]SearchResult, 0, min(len(candidates), limit))
	for i, c := range candidates {
		if i >= limit {
			break
		}
		c.result.Score = c.vectorScore
		c.result.ScoreKind = "similar"
		results = append(results, c.result)
	}
	s.hydrateResultContents(ctx, results)
	return results, nil
}

// hydrateResultContents fills ChunkContent for final results. Candidate
// collection deliberately skips the content column so that scoring never
// drags full chunk bodies out of SQLite for rows that get discarded; only
// the chunks that survive ranking are loaded here.
func (s *Store) hydrateResultContents(ctx context.Context, results []SearchResult) {
	if len(results) == 0 {
		return
	}
	placeholders := make([]string, len(results))
	args := make([]any, len(results))
	for i := range results {
		placeholders[i] = "?"
		args[i] = results[i].ChunkID
	}
	//nolint:gosec // placeholders are all literal "?" - no user input in the query string
	query := `SELECT id, content FROM chunks WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		logx.Warn("search: loading result contents failed", "err", err)
		return
	}
	defer func() { _ = rows.Close() }()

	contents := make(map[int64]string, len(results))
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			logx.Warn("search: scanning result contents failed", "err", err)
			return
		}
		contents[id] = content
	}
	if err := rows.Err(); err != nil {
		logx.Warn("search: reading result contents failed", "err", err)
	}
	for i := range results {
		if content, ok := contents[results[i].ChunkID]; ok {
			results[i].ChunkContent = content
		}
	}
}

func (s *Store) canRunVectorFallback(ctx context.Context, pathPrefix string, metadataWhere string, metadataArgs []any) (bool, error) {
	if s.maxVectorSearchCandidates == 0 {
		logx.Info("skipping brute-force vector fallback", "reason", "max_vector_candidates=0")
		return false, nil
	}
	if s.maxVectorSearchCandidates < 0 {
		return true, nil
	}

	var count int
	query := `SELECT COUNT(*) FROM chunks c JOIN documents d ON c.document_id = d.id WHERE 1=1`
	args := make([]any, 0, 1+len(metadataArgs))
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		query += ` AND d.path LIKE ? ESCAPE '\'`
		args = append(args, pathPattern)
	}
	query += metadataWhere // #nosec G202
	args = append(args, metadataArgs...)
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false, err
	}

	if count > s.maxVectorSearchCandidates {
		logx.Info("skipping brute-force vector fallback", "candidate_count_over", s.maxVectorSearchCandidates, "path_prefix", pathPrefix)
		return false, nil
	}
	return true, nil
}

// collectVectorCandidates gathers vector-only candidates (not already in keywordCandidates)
// and returns them keyed by chunk ID for unified RRF fusion.
func (s *Store) collectVectorCandidates(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, keywordCandidates map[int]*searchCandidate, docFilter map[string]float32, metadataWhere string, metadataArgs []any) (map[int]*searchCandidate, error) {
	vectorOnly := make(map[int]*searchCandidate)

	if s.hnsw != nil && s.hnsw.ready.Load() {
		if pathPrefix == "" && metadataWhere == "" {
			s.collectHNSWCandidates(ctx, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
		} else {
			s.collectHNSWCandidatesWithDBFilter(ctx, queryEmbedding, limit, pathPrefix, metadataWhere, metadataArgs, keywordCandidates, vectorOnly)
		}
		return vectorOnly, nil
	}

	if ok, err := s.canRunVectorFallback(ctx, pathPrefix, metadataWhere, metadataArgs); err != nil {
		return nil, err
	} else if !ok {
		return vectorOnly, nil
	}

	var rows *sql.Rows
	var err error
	if pathPrefix == "" && len(docFilter) > 0 && metadataWhere == "" {
		rows, err = s.queryChunksByDocPaths(ctx, docFilter)
	} else {
		query := `SELECT c.id, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id
			 WHERE 1=1`
		args := make([]any, 0, 1+len(metadataArgs))

		pathPattern := sqlLikePrefixPattern(pathPrefix)
		if pathPrefix != "" {
			query += ` AND d.path LIKE ? ESCAPE '\'`
			args = append(args, pathPattern)
		}
		query += metadataWhere // #nosec G202
		args = append(args, metadataArgs...)
		rows, err = s.db.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanVectorRows(rows, queryEmbedding, limit, keywordCandidates, vectorOnly)
}

func (s *Store) buildMetadataFilter(filter SearchFilter) (string, []any) {
	if len(filter.FileTypes) == 0 && len(filter.Languages) == 0 && len(filter.Tags) == 0 && filter.Collection == "" {
		return "", nil
	}

	var conds []string
	var args []any

	if len(filter.FileTypes) > 0 {
		placeholders := make([]string, len(filter.FileTypes))
		for i, ft := range filter.FileTypes {
			placeholders[i] = "?"
			args = append(args, ft)
		}
		conds = append(conds, "d.file_type IN ("+strings.Join(placeholders, ",")+")")
	}

	if len(filter.Languages) > 0 {
		placeholders := make([]string, len(filter.Languages))
		for i, lang := range filter.Languages {
			placeholders[i] = "?"
			args = append(args, lang)
		}
		conds = append(conds, "d.language IN ("+strings.Join(placeholders, ",")+")")
	}

	// Tags are stored as a JSON object ('' when absent). json_each matches
	// key/value pairs exactly, unlike a substring LIKE which is confused by
	// wildcard characters and JSON escaping.
	for k, v := range filter.Tags {
		conds = append(conds, `(json_valid(d.tags) AND EXISTS (SELECT 1 FROM json_each(d.tags) WHERE json_each.key = ? AND json_each.value = ?))`)
		args = append(args, k, v)
	}

	if filter.Collection != "" {
		conds = append(conds, "d.collection = ?")
		args = append(args, filter.Collection)
	}

	return " AND " + strings.Join(conds, " AND "), args
}

func (s *Store) collectFTSCandidatesFiltered(ctx context.Context, ftsQuery string, queryEmbedding []float32, candidateLimit int, pathPrefix string, rankOffset int, candidates map[int]*searchCandidate, metadataWhere string, metadataArgs []any) (int, error) {
	baseQuery := `SELECT c.id, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
			 FROM chunks_fts
			 JOIN chunks c ON c.id = chunks_fts.rowid
			 JOIN documents d ON c.document_id = d.id
			 WHERE chunks_fts MATCH ?`

	var rows *sql.Rows
	var err error
	args := []any{ftsQuery}

	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		baseQuery += " AND d.path LIKE ? ESCAPE '\\'"
		args = append(args, pathPattern)
	}

	baseQuery += metadataWhere // #nosec G202
	args = append(args, metadataArgs...)

	baseQuery += " ORDER BY bm25(chunks_fts), d.path, c.chunk_index, c.id LIMIT ?"
	args = append(args, candidateLimit)

	rows, err = s.db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	rank := 0
	for rows.Next() {
		var id int
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		var modifiedAt time.Time
		var parentID *int64
		var depth int
		var sectionTitle string
		if err := rows.Scan(&id, &chunkIndex, &embeddingBytes, &docPath, &modifiedAt, &parentID, &depth, &sectionTitle); err != nil {
			return 0, err
		}
		rank++
		keywordRank := rankOffset + rank
		if existing, ok := candidates[id]; ok {
			if keywordRank < existing.keywordRank {
				existing.keywordRank = keywordRank
			}
			continue
		}
		candidates[id] = &searchCandidate{
			id: id,
			result: SearchResult{
				DocumentPath: docPath,
				ChunkIndex:   chunkIndex,
				ScoreKind:    "rrf",
				ParentID:     parentID,
				Depth:        depth,
				SectionTitle: sectionTitle,
			},
			keywordRank: keywordRank,
			vectorScore: dotProductEncoded(queryEmbedding, embeddingBytes),
			hasVector:   len(queryEmbedding) > 0 && len(embeddingBytes) > 0,
			modifiedAt:  modifiedAt,
		}
	}
	return rank, rows.Err()
}

func (s *Store) scanVectorRows(rows *sql.Rows, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate) (map[int]*searchCandidate, error) {
	return s.scanVectorRowsWithDocFilter(rows, queryEmbedding, limit, keywordCandidates, vectorOnly, nil)
}

func (s *Store) collectHNSWCandidates(ctx context.Context, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) {
	fetchK := limit*3 + len(keywordCandidates) + 10
	ids := s.hnsw.Search(queryEmbedding, fetchK)
	if len(ids) == 0 {
		return
	}

	s.loadHNSWChunkRows(ctx, ids, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
}

func (s *Store) collectHNSWCandidatesWithDBFilter(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, metadataWhere string, metadataArgs []any, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate) {
	// Fast path: probe the global nearest neighbors and keep those that pass
	// the SQL filter. When the filter is broad (a repo-root path prefix, a
	// common file type) enough survive, and the survivors are exactly the
	// nearest filtered chunks — the full filter-set id scan below is skipped.
	probeK := limit*3 + len(keywordCandidates) + 10
	probeIDs := s.hnsw.Search(queryEmbedding, probeK)
	if len(probeIDs) > 0 {
		survivors, err := s.filterChunkIDs(ctx, probeIDs, pathPrefix, metadataWhere, metadataArgs)
		if err != nil {
			logx.Warn("filtered vector search: probe filter query failed; returning keyword-only results", "err", err)
			return
		}
		if len(survivors) > 0 {
			s.loadHNSWChunkRows(ctx, survivors, queryEmbedding, limit, keywordCandidates, vectorOnly, nil)
		}
		if len(survivors) >= limit {
			return
		}
	}

	query := `SELECT c.id FROM chunks c JOIN documents d ON c.document_id = d.id WHERE 1=1`
	args := make([]any, 0, 1+len(metadataArgs))
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		query += ` AND d.path LIKE ? ESCAPE '\'`
		args = append(args, pathPattern)
	}
	query += metadataWhere // #nosec G202
	args = append(args, metadataArgs...)

	filterRows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		logx.Warn("filtered vector search: candidate query failed; returning keyword-only results", "err", err)
		return
	}
	filterSet := make(map[int]bool)
	for filterRows.Next() {
		var id int
		if filterRows.Scan(&id) == nil {
			filterSet[id] = true
		}
	}
	_ = filterRows.Close()
	if rowsErr := filterRows.Err(); rowsErr != nil {
		logx.Warn("filtered vector search: candidate scan failed; returning keyword-only results", "err", rowsErr)
		return
	}
	if len(filterSet) == 0 {
		return
	}

	// Ask HNSW for nearest neighbors and intersect with the SQLite filter set.
	fetchK := limit*3 + len(keywordCandidates) + len(filterSet)
	hnswIDs := s.hnsw.Search(queryEmbedding, fetchK)

	var filtered []int
	for _, id := range hnswIDs {
		if filterSet[id] {
			filtered = append(filtered, id)
		}
	}

	if len(filtered) > 0 {
		s.loadHNSWChunkRows(ctx, filtered, queryEmbedding, limit, keywordCandidates, vectorOnly, nil)
	}
	if len(filtered) >= limit {
		return
	}

	if s.maxVectorSearchCandidates == 0 {
		logx.Info("skipping brute-force filtered vector fallback", "reason", "max_vector_candidates=0", "path_prefix", pathPrefix)
		return
	}
	if s.maxVectorSearchCandidates > 0 && len(filterSet) > s.maxVectorSearchCandidates {
		logx.Info("skipping brute-force filtered vector fallback", "matching_chunks", len(filterSet), "max_vector_candidates", s.maxVectorSearchCandidates, "path_prefix", pathPrefix)
		return
	}
	allFilteredIDs := make([]int, 0, len(filterSet))
	for id := range filterSet {
		allFilteredIDs = append(allFilteredIDs, id)
	}
	s.loadHNSWChunkRows(ctx, allFilteredIDs, queryEmbedding, limit, keywordCandidates, vectorOnly, nil)
}

// filterChunkIDs returns the subset of ids whose chunks pass the path-prefix
// and metadata filters. Result order is unspecified; callers re-score by exact
// dot product anyway.
func (s *Store) filterChunkIDs(ctx context.Context, ids []int, pathPrefix string, metadataWhere string, metadataArgs []any) ([]int, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1+len(metadataArgs))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	//nolint:gosec // placeholders are all literal "?" - no user input in the query string
	query := `SELECT c.id FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE c.id IN (` + strings.Join(placeholders, ",") + `)`
	if pathPrefix != "" {
		query += ` AND d.path LIKE ? ESCAPE '\'`
		args = append(args, sqlLikePrefixPattern(pathPrefix))
	}
	query += metadataWhere // #nosec G202
	args = append(args, metadataArgs...)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]int, 0, len(ids))
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) loadHNSWChunkRows(ctx context.Context, ids []int, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) {
	if len(ids) == 0 {
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	//nolint:gosec // placeholders are all literal "?" - no user input in the query string
	query := `SELECT c.id, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE c.id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		logx.Warn("vector search: loading hnsw candidate chunks failed", "err", err)
		return
	}
	defer func() { _ = rows.Close() }()

	if _, err := s.scanVectorRowsWithDocFilter(rows, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter); err != nil {
		logx.Warn("vector search: scanning hnsw candidate chunks failed", "err", err)
		return
	}
}

func (s *Store) queryChunksByDocPaths(ctx context.Context, docPaths map[string]float32) (*sql.Rows, error) {
	paths := make([]string, 0, len(docPaths))
	for p := range docPaths {
		paths = append(paths, p)
	}
	placeholders := make([]string, len(paths))
	args := make([]any, len(paths))
	for i, p := range paths {
		placeholders[i] = "?"
		args[i] = p
	}
	query := `SELECT c.id, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE d.path IN (` + strings.Join(placeholders, ",") + `)`
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) scanVectorRowsWithDocFilter(rows *sql.Rows, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) (map[int]*searchCandidate, error) {
	top := make(candidateHeap, 0, limit)
	for rows.Next() {
		var id int
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		var modifiedAt time.Time
		var parentID *int64
		var depth int
		var sectionTitle string
		if err := rows.Scan(&id, &chunkIndex, &embeddingBytes, &docPath, &modifiedAt, &parentID, &depth, &sectionTitle); err != nil {
			return nil, err
		}
		if _, ok := keywordCandidates[id]; ok {
			continue
		}
		if len(docFilter) > 0 {
			if _, ok := docFilter[docPath]; !ok {
				continue
			}
		}

		score := dotProductEncoded(queryEmbedding, embeddingBytes)
		candidate := scoredResult{
			id:           id,
			path:         docPath,
			score:        score,
			chunkIndex:   chunkIndex,
			modifiedAt:   modifiedAt,
			parentID:     parentID,
			depth:        depth,
			sectionTitle: sectionTitle,
		}

		if len(top) < limit {
			heap.Push(&top, candidate)
		} else if scoredResultBetter(candidate, top[0]) {
			top[0] = candidate
			heap.Fix(&top, 0)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, sr := range top {
		vectorOnly[sr.id] = &searchCandidate{
			id: sr.id,
			result: SearchResult{
				DocumentPath: sr.path,
				ChunkIndex:   sr.chunkIndex,
				ScoreKind:    "rrf",
				ParentID:     sr.parentID,
				Depth:        sr.depth,
				SectionTitle: sr.sectionTitle,
			},
			vectorScore: sr.score,
			hasVector:   true,
			modifiedAt:  sr.modifiedAt,
		}
	}
	return vectorOnly, nil
}
