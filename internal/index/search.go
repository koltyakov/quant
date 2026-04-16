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
	if queryEmbedding != nil {
		docFilter = s.docEmbeds.topDocPaths(queryEmbedding, docFilterTopK)
	}

	var vectorOnlyCandidates map[int]*searchCandidate
	if queryEmbedding != nil {
		var err error
		vectorOnlyCandidates, err = s.collectVectorCandidates(ctx, queryEmbedding, limit, pathPrefix, keywordCandidates, docFilter)
		if err != nil {
			return nil, err
		}
	}

	weights := classifyQueryWeights(query, s.keywordWeightOverride, s.vectorWeightOverride)
	results := unifiedRRF(keywordCandidates, vectorOnlyCandidates, limit, pathQueryTokens(query), weights)

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
		c.result.ScoreKind = "similar"
		results = append(results, c.result)
	}
	return results, nil
}

func (s *Store) canRunVectorFallback(ctx context.Context, pathPrefix string) (bool, error) {
	if s.maxVectorSearchCandidates == 0 {
		logx.Info("skipping brute-force vector fallback", "reason", "max_vector_candidates=0")
		return false, nil
	}
	if s.maxVectorSearchCandidates < 0 {
		return true, nil
	}

	var count int
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM chunks c JOIN documents d ON c.document_id = d.id WHERE d.path LIKE ? ESCAPE '\'`,
			pathPattern,
		).Scan(&count)
		if err != nil {
			return false, err
		}
	} else {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&count)
		if err != nil {
			return false, err
		}
	}

	if count > s.maxVectorSearchCandidates {
		logx.Info("skipping brute-force vector fallback", "candidate_count_over", s.maxVectorSearchCandidates, "path_prefix", pathPrefix)
		return false, nil
	}
	return true, nil
}

// collectVectorCandidates gathers vector-only candidates (not already in keywordCandidates)
// and returns them keyed by chunk ID for unified RRF fusion.
func (s *Store) collectVectorCandidates(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, keywordCandidates map[int]*searchCandidate, docFilter map[string]float32) (map[int]*searchCandidate, error) {
	vectorOnly := make(map[int]*searchCandidate)

	if s.hnsw != nil && s.hnsw.ready.Load() {
		if pathPrefix == "" {
			s.collectHNSWCandidates(ctx, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
		} else {
			s.collectHNSWCandidatesWithPrefix(ctx, queryEmbedding, limit, pathPrefix, keywordCandidates, vectorOnly, docFilter)
		}
		return vectorOnly, nil
	}

	if ok, err := s.canRunVectorFallback(ctx, pathPrefix); err != nil {
		return nil, err
	} else if !ok {
		return vectorOnly, nil
	}

	var rows *sql.Rows
	var err error
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id
			 WHERE d.path LIKE ? ESCAPE '\'`,
			pathPattern,
		)
	} else if len(docFilter) > 0 {
		rows, err = s.queryChunksByDocPaths(ctx, docFilter)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id`,
		)
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

	for k, v := range filter.Tags {
		conds = append(conds, `d.tags LIKE ?`)
		pattern := `%"` + k + `":"` + v + `"%`
		args = append(args, pattern)
	}

	if filter.Collection != "" {
		conds = append(conds, "d.collection = ?")
		args = append(args, filter.Collection)
	}

	return " AND " + strings.Join(conds, " AND "), args
}

func (s *Store) collectFTSCandidatesFiltered(ctx context.Context, ftsQuery string, queryEmbedding []float32, candidateLimit int, pathPrefix string, rankOffset int, candidates map[int]*searchCandidate, metadataWhere string, metadataArgs []any) (int, error) {
	baseQuery := `SELECT c.id, c.content, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
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

	baseQuery += " ORDER BY bm25(chunks_fts) LIMIT ?"
	args = append(args, candidateLimit)

	rows, err = s.db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	rank := 0
	for rows.Next() {
		var id int
		var content string
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		var modifiedAt time.Time
		var parentID *int64
		var depth int
		var sectionTitle string
		if err := rows.Scan(&id, &content, &chunkIndex, &embeddingBytes, &docPath, &modifiedAt, &parentID, &depth, &sectionTitle); err != nil {
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
				ChunkContent: content,
				ChunkIndex:   chunkIndex,
				ScoreKind:    "rrf",
				ParentID:     parentID,
				Depth:        depth,
				SectionTitle: sectionTitle,
			},
			keywordRank: keywordRank,
			vectorScore: dotProductEncoded(queryEmbedding, embeddingBytes),
			modifiedAt:  modifiedAt,
		}
	}
	return rank, rows.Err()
}

func (s *Store) scanVectorRows(rows *sql.Rows, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate) (map[int]*searchCandidate, error) {
	top := make(candidateHeap, 0, limit)
	for rows.Next() {
		var id int
		var content string
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		var modifiedAt time.Time
		var parentID *int64
		var depth int
		var sectionTitle string
		if err := rows.Scan(&id, &content, &chunkIndex, &embeddingBytes, &docPath, &modifiedAt, &parentID, &depth, &sectionTitle); err != nil {
			return nil, err
		}
		if _, ok := keywordCandidates[id]; ok {
			continue
		}

		score := dotProductEncoded(queryEmbedding, embeddingBytes)
		candidate := scoredResult{
			id:           id,
			path:         docPath,
			score:        score,
			content:      content,
			chunkIndex:   chunkIndex,
			modifiedAt:   modifiedAt,
			parentID:     parentID,
			depth:        depth,
			sectionTitle: sectionTitle,
		}

		if len(top) < limit {
			heap.Push(&top, candidate)
		} else if candidate.score > top[0].score {
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
				ChunkContent: sr.content,
				ChunkIndex:   sr.chunkIndex,
				ScoreKind:    "rrf",
				ParentID:     sr.parentID,
				Depth:        sr.depth,
				SectionTitle: sr.sectionTitle,
			},
			vectorScore: sr.score,
			modifiedAt:  sr.modifiedAt,
		}
	}
	return vectorOnly, nil
}

func (s *Store) collectHNSWCandidates(ctx context.Context, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) {
	fetchK := limit*3 + len(keywordCandidates) + 10
	ids := s.hnsw.Search(queryEmbedding, fetchK)
	if len(ids) == 0 {
		return
	}

	s.loadHNSWChunkRows(ctx, ids, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
}

func (s *Store) collectHNSWCandidatesWithPrefix(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) {
	// Get chunk IDs matching the prefix from SQLite (cheap index scan).
	pathPattern := sqlLikePrefixPattern(pathPrefix)
	prefixRows, err := s.db.QueryContext(ctx,
		`SELECT c.id FROM chunks c JOIN documents d ON c.document_id = d.id WHERE d.path LIKE ? ESCAPE '\'`,
		pathPattern,
	)
	if err != nil {
		return
	}
	prefixSet := make(map[int]bool)
	for prefixRows.Next() {
		var id int
		if prefixRows.Scan(&id) == nil {
			prefixSet[id] = true
		}
	}
	_ = prefixRows.Close()
	if prefixRows.Err() != nil {
		return
	}
	if len(prefixSet) == 0 {
		return
	}

	// Ask HNSW for nearest neighbors and intersect with prefix set.
	fetchK := limit*3 + len(keywordCandidates) + len(prefixSet)
	hnswIDs := s.hnsw.Search(queryEmbedding, fetchK)

	var filtered []int
	for _, id := range hnswIDs {
		if prefixSet[id] {
			filtered = append(filtered, id)
		}
	}

	// If HNSW intersection yielded enough results, load their full rows.
	if len(filtered) >= limit {
		s.loadHNSWChunkRows(ctx, filtered, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
		return
	}

	if s.maxVectorSearchCandidates > 0 && len(prefixSet) > s.maxVectorSearchCandidates {
		logx.Info("skipping brute-force prefix vector fallback", "prefix_chunks", len(prefixSet), "max_vector_candidates", s.maxVectorSearchCandidates, "path_prefix", pathPrefix)
		return
	}
	allPrefixIDs := make([]int, 0, len(prefixSet))
	for id := range prefixSet {
		allPrefixIDs = append(allPrefixIDs, id)
	}
	s.loadHNSWChunkRows(ctx, allPrefixIDs, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter)
}

func (s *Store) loadHNSWChunkRows(ctx context.Context, ids []int, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	//nolint:gosec // placeholders are all literal "?" - no user input in the query string
	query := `SELECT c.id, c.content, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE c.id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	if _, err := s.scanVectorRowsWithDocFilter(rows, queryEmbedding, limit, keywordCandidates, vectorOnly, docFilter); err != nil {
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
	query := `SELECT c.id, c.content, c.chunk_index, c.embedding, d.path, d.modified_at, c.parent_id, c.depth, c.section_title
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE d.path IN (` + strings.Join(placeholders, ",") + `)`
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) scanVectorRowsWithDocFilter(rows *sql.Rows, queryEmbedding []float32, limit int, keywordCandidates map[int]*searchCandidate, vectorOnly map[int]*searchCandidate, docFilter map[string]float32) (map[int]*searchCandidate, error) {
	top := make(candidateHeap, 0, limit)
	for rows.Next() {
		var id int
		var content string
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		var modifiedAt time.Time
		var parentID *int64
		var depth int
		var sectionTitle string
		if err := rows.Scan(&id, &content, &chunkIndex, &embeddingBytes, &docPath, &modifiedAt, &parentID, &depth, &sectionTitle); err != nil {
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
			content:      content,
			chunkIndex:   chunkIndex,
			modifiedAt:   modifiedAt,
			parentID:     parentID,
			depth:        depth,
			sectionTitle: sectionTitle,
		}

		if len(top) < limit {
			heap.Push(&top, candidate)
		} else if candidate.score > top[0].score {
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
				ChunkContent: sr.content,
				ChunkIndex:   sr.chunkIndex,
				ScoreKind:    "rrf",
				ParentID:     sr.parentID,
				Depth:        sr.depth,
				SectionTitle: sr.sectionTitle,
			},
			vectorScore: sr.score,
			modifiedAt:  sr.modifiedAt,
		}
	}
	return vectorOnly, nil
}
