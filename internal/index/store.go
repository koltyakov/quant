package index

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	dbPath string
	backup string // non-empty if a pre-existing DB was backed up due to migration failure
}

// NewStore opens (or creates) a SQLite database at dbPath.
// If the database exists but migration fails, the old file is backed up and a
// fresh database is created. Call RemoveBackup after re-indexing completes.
func NewStore(dbPath string) (*Store, error) {
	s, err := openStore(dbPath)
	if err == nil {
		return s, nil
	}

	// If the file doesn't exist the error is not recoverable by recreating.
	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		return nil, err
	}

	// Back up the broken DB and start fresh.
	backupPath := dbPath + ".bak"
	log.Printf("Migration failed (%v); backing up existing database to %s", err, backupPath)

	// Remove stale backup if present, then rename current DB + WAL/SHM files.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(backupPath + suffix)
		_ = os.Rename(dbPath+suffix, backupPath+suffix)
	}

	s, err = openStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("creating fresh database after backup: %w", err)
	}
	s.backup = backupPath
	return s, nil
}

// RemoveBackup deletes the backup created during NewStore, if any.
func (s *Store) RemoveBackup() {
	if s.backup == "" {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(s.backup + suffix)
	}
	log.Printf("Removed database backup %s", s.backup)
	s.backup = ""
}

func openStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	conns := runtime.GOMAXPROCS(0)
	if conns < 4 {
		conns = 4
	}
	if conns > 16 {
		conns = 16
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns / 2)

	s := &Store{db: db, dbPath: dbPath}
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA temp_store = MEMORY`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA mmap_size = 268435456`,
		`PRAGMA cache_size = -64000`,
	} {
		if _, err := s.db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configuring sqlite pragma %q: %w", pragma, err)
		}
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS documents (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		path        TEXT NOT NULL UNIQUE,
		hash        TEXT NOT NULL,
		modified_at DATETIME NOT NULL,
		indexed_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS chunks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content     TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		embedding   BLOB NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_chunks_document_chunk ON chunks(document_id, chunk_index);
	CREATE INDEX IF NOT EXISTS idx_chunks_document_id ON chunks(document_id);
	CREATE TABLE IF NOT EXISTS embedding_metadata (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		content,
		content='chunks',
		content_rowid='id',
		tokenize='porter unicode61'
	);
	CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
	END;
	CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
	END;
	CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
		INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
	END;
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) UpsertDocument(ctx context.Context, doc *Document) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO documents (path, hash, modified_at, indexed_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(path) DO UPDATE SET
			hash = excluded.hash,
			modified_at = excluded.modified_at,
			indexed_at = CURRENT_TIMESTAMP
		 RETURNING id`,
		doc.Path, doc.Hash, doc.ModifiedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting document: %w", err)
	}
	return id, nil
}

func (s *Store) InsertChunk(ctx context.Context, chunk *ChunkRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chunks (document_id, content, chunk_index, embedding) VALUES (?, ?, ?, ?)`,
		chunk.DocumentID, chunk.Content, chunk.ChunkIndex, chunk.Embedding,
	)
	return err
}

func (s *Store) ReindexDocument(ctx context.Context, doc *Document, chunks []ChunkRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	docID, err := upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("deleting existing chunks: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks (document_id, content, chunk_index, embedding) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing chunk insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, chunk := range chunks {
		if _, err := stmt.ExecContext(ctx,
			docID, chunk.Content, chunk.ChunkIndex, chunk.Embedding,
		); err != nil {
			return fmt.Errorf("inserting chunk %d: %w", chunk.ChunkIndex, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

func (s *Store) DeleteChunksByDocument(ctx context.Context, docID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID)
	return err
}

func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE path = ?`, path)
	return err
}

func (s *Store) DeleteDocumentsByPrefix(ctx context.Context, prefix string) error {
	prefix = filepath.Clean(prefix)
	if prefix == "." || prefix == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM documents`)
		return err
	}

	likePrefix := prefix + string(filepath.Separator) + "%"
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM documents WHERE path = ? OR path LIKE ?`,
		prefix, likePrefix,
	)
	return err
}

func (s *Store) RenameDocumentPath(ctx context.Context, oldPath, newPath string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET path = ? WHERE path = ?`, newPath, oldPath)
	return err
}

func (s *Store) EnsureEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) (bool, error) {
	current, err := s.embeddingMetadata(ctx)
	if err != nil {
		return false, err
	}

	if current == nil {
		docCount, chunkCount, err := s.Stats(ctx)
		if err != nil {
			return false, err
		}
		needsReset := docCount > 0 || chunkCount > 0
		if needsReset {
			if err := s.resetIndex(ctx); err != nil {
				return false, err
			}
		}
		if err := s.putEmbeddingMetadata(ctx, meta); err != nil {
			return false, err
		}
		return needsReset, nil
	}

	if *current == meta {
		return false, nil
	}

	if err := s.resetIndex(ctx); err != nil {
		return false, err
	}
	if err := s.putEmbeddingMetadata(ctx, meta); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) GetDocumentByPath(ctx context.Context, path string) (*Document, error) {
	doc := &Document{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, path, hash, modified_at, indexed_at FROM documents WHERE path = ?`,
		path,
	).Scan(&doc.ID, &doc.Path, &doc.Hash, &doc.ModifiedAt, &doc.IndexedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path, hash, modified_at, indexed_at FROM documents ORDER BY path`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var docs []Document
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.Path, &doc.Hash, &doc.ModifiedAt, &doc.IndexedAt); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (docCount int, chunkCount int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&docCount)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&chunkCount)
	return docCount, chunkCount, err
}

type searchCandidate struct {
	id          int
	result      SearchResult
	keywordRank int
	vectorScore float32
}

// Search performs hybrid search combining FTS5 keyword prefilter with vector reranking.
// pathPrefix optionally restricts results to documents whose path starts with the given prefix.
// It merges candidates from AND and OR FTS queries, reranks them together, then fills any
// remaining result slots with vector-only matches.
func (s *Store) Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	andQuery, orQuery := buildFTSQueries(query)
	keywordCandidates := make(map[int]*searchCandidate)
	rankOffset := 0
	candidateLimit := searchCandidateLimit(limit)

	if andQuery != "" {
		collected, err := s.collectFTSCandidates(ctx, andQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
		rankOffset += collected
	}

	if orQuery != "" && orQuery != andQuery {
		_, err := s.collectFTSCandidates(ctx, orQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
	}

	keywordResults := rerankKeywordCandidates(keywordCandidates, limit)
	if len(keywordResults) >= limit {
		return keywordResults, nil
	}

	vectorResults, err := s.searchVector(ctx, queryEmbedding, limit-len(keywordResults), pathPrefix, keywordCandidates)
	if err != nil {
		return nil, err
	}

	if len(keywordResults) == 0 {
		return vectorResults, nil
	}
	return append(keywordResults, vectorResults...), nil
}

func searchCandidateLimit(limit int) int {
	candidateLimit := limit * 20
	if candidateLimit < 50 {
		candidateLimit = 50
	}
	return candidateLimit
}

func (s *Store) collectFTSCandidates(ctx context.Context, ftsQuery string, queryEmbedding []float32, candidateLimit int, pathPrefix string, rankOffset int, candidates map[int]*searchCandidate) (int, error) {
	var rows *sql.Rows
	var err error
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path
			 FROM chunks_fts
			 JOIN chunks c ON c.id = chunks_fts.rowid
			 JOIN documents d ON c.document_id = d.id
			 WHERE chunks_fts MATCH ? AND d.path LIKE ? ESCAPE '\'
			 ORDER BY bm25(chunks_fts), c.chunk_index
			 LIMIT ?`,
			ftsQuery, pathPattern, candidateLimit,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path
			 FROM chunks_fts
			 JOIN chunks c ON c.id = chunks_fts.rowid
			 JOIN documents d ON c.document_id = d.id
			 WHERE chunks_fts MATCH ?
			 ORDER BY bm25(chunks_fts), c.chunk_index
			 LIMIT ?`,
			ftsQuery, candidateLimit,
		)
	}
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
		if err := rows.Scan(&id, &content, &chunkIndex, &embeddingBytes, &docPath); err != nil {
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
			},
			keywordRank: keywordRank,
			vectorScore: dotProductEncoded(queryEmbedding, embeddingBytes),
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return rank, nil
}

// searchVector performs a brute-force vector similarity search over all matching chunks.
func (s *Store) searchVector(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, exclude map[int]*searchCandidate) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	var rows *sql.Rows
	var err error
	if pathPrefix != "" {
		pathPattern := sqlLikePrefixPattern(pathPrefix)
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id
			 WHERE d.path LIKE ? ESCAPE '\'`,
			pathPattern,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT c.id, c.content, c.chunk_index, c.embedding, d.path
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return rerankByVector(rows, queryEmbedding, limit, exclude)
}

// rerankKeywordCandidates combines keyword rank with vector similarity rank
// using Reciprocal Rank Fusion: score = 1/(k+keywordRank) + 1/(k+vectorRank), k=60.
func rerankKeywordCandidates(candidates map[int]*searchCandidate, limit int) []SearchResult {
	if len(candidates) == 0 || limit <= 0 {
		return nil
	}

	ranked := make([]*searchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, candidate)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].vectorScore == ranked[j].vectorScore {
			return ranked[i].keywordRank < ranked[j].keywordRank
		}
		return ranked[i].vectorScore > ranked[j].vectorScore
	})

	const k = 60
	top := make(candidateHeap, 0, limit)
	for i, candidate := range ranked {
		vectorRank := i + 1
		rrf := 1.0/float32(k+candidate.keywordRank) + 1.0/float32(k+vectorRank)
		result := candidate.result
		result.Score = rrf
		sr := scoredResult{result: result, score: rrf}
		if len(top) < limit {
			heap.Push(&top, sr)
		} else if rrf > top[0].score {
			top[0] = sr
			heap.Fix(&top, 0)
		}
	}

	sort.Slice(top, func(i, j int) bool { return top[i].score > top[j].score })
	results := make([]SearchResult, len(top))
	for i := range top {
		results[i] = top[i].result
	}
	return results
}

// rerankByVector computes dot product scores for candidate rows and returns top results.
func rerankByVector(rows *sql.Rows, queryEmbedding []float32, limit int, exclude map[int]*searchCandidate) ([]SearchResult, error) {
	candidates := make(candidateHeap, 0, limit)
	for rows.Next() {
		var id int
		var content string
		var chunkIndex int
		var embeddingBytes []byte
		var docPath string
		if err := rows.Scan(&id, &content, &chunkIndex, &embeddingBytes, &docPath); err != nil {
			return nil, err
		}
		if _, ok := exclude[id]; ok {
			continue
		}

		score := dotProductEncoded(queryEmbedding, embeddingBytes)
		candidate := scoredResult{
			result: SearchResult{
				DocumentPath: docPath,
				ChunkContent: content,
				ChunkIndex:   chunkIndex,
				Score:        score,
				ScoreKind:    "cosine",
			},
			score: score,
		}

		if limit <= 0 {
			continue
		}
		if len(candidates) < limit {
			heap.Push(&candidates, candidate)
			continue
		}
		if candidate.score <= candidates[0].score {
			continue
		}
		candidates[0] = candidate
		heap.Fix(&candidates, 0)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]SearchResult, len(candidates))
	for i := range candidates {
		results[i] = candidates[i].result
	}
	return results, nil
}

type scoredResult struct {
	result SearchResult
	score  float32
}

type candidateHeap []scoredResult

func (h candidateHeap) Len() int           { return len(h) }
func (h candidateHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h candidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x any) {
	*h = append(*h, x.(scoredResult))
}

func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func EncodeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func NormalizeFloat32(vec []float32) []float32 {
	normalized := make([]float32, len(vec))
	copy(normalized, vec)

	var norm float32
	for _, v := range normalized {
		norm += v * v
	}
	if norm == 0 {
		return normalized
	}

	scale := 1 / sqrt32(norm)
	for i := range normalized {
		normalized[i] *= scale
	}
	return normalized
}

func decodeFloat32(data []byte) []float32 {
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

func dotProduct(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

func dotProductEncoded(query []float32, encoded []byte) float32 {
	if len(encoded) != len(query)*4 {
		return 0
	}

	var dot float32
	for i, q := range query {
		v := math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
		dot += q * v
	}
	return dot
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}

func sqlLikePrefixPattern(prefix string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return replacer.Replace(prefix) + "%"
}

func upsertDocumentTx(ctx context.Context, tx *sql.Tx, doc *Document) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO documents (path, hash, modified_at, indexed_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(path) DO UPDATE SET
			hash = excluded.hash,
			modified_at = excluded.modified_at,
			indexed_at = CURRENT_TIMESTAMP
		 RETURNING id`,
		doc.Path, doc.Hash, doc.ModifiedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting document: %w", err)
	}
	return id, nil
}

func (s *Store) embeddingMetadata(ctx context.Context) (*EmbeddingMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM embedding_metadata`)
	if err != nil {
		return nil, fmt.Errorf("querying embedding metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	values := make(map[string]string)
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scanning embedding metadata: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading embedding metadata: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}

	dims, err := strconv.Atoi(values["dimensions"])
	if err != nil {
		return nil, fmt.Errorf("parsing embedding dimensions: %w", err)
	}

	return &EmbeddingMetadata{
		Model:      values["model"],
		Dimensions: dims,
		Normalized: values["normalized"] == "true",
	}, nil
}

func (s *Store) putEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM embedding_metadata`); err != nil {
		return fmt.Errorf("clearing embedding metadata: %w", err)
	}

	values := map[string]string{
		"model":      meta.Model,
		"dimensions": strconv.Itoa(meta.Dimensions),
		"normalized": strconv.FormatBool(meta.Normalized),
		"schema":     "1",
	}
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_metadata(key, value) VALUES(?, ?)`, key, value); err != nil {
			return fmt.Errorf("writing embedding metadata %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing metadata transaction: %w", err)
	}
	return nil
}

func (s *Store) resetIndex(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning reset transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM documents`); err != nil {
		return fmt.Errorf("clearing documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuilding chunks fts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing reset transaction: %w", err)
	}
	return nil
}

var ftsTokenPattern = regexp.MustCompile(`[\pL\pN_]+`)

// buildFTSQueries converts a natural-language query into AND and OR FTS5 queries.
// The AND query requires all terms to match (tighter); the OR query matches any term.
// When there is only one token, both queries are identical.
func buildFTSQueries(query string) (andQuery, orQuery string) {
	// Extract quoted phrases first.
	var phrases []string
	remaining := query
	for {
		start := strings.Index(remaining, `"`)
		if start == -1 {
			break
		}
		end := strings.Index(remaining[start+1:], `"`)
		if end == -1 {
			break
		}
		phrase := strings.TrimSpace(remaining[start+1 : start+1+end])
		if phrase != "" {
			phrases = append(phrases, `"`+phrase+`"`)
		}
		remaining = remaining[:start] + " " + remaining[start+1+end+1:]
	}

	// Tokenize the remaining (non-phrase) text.
	matches := ftsTokenPattern.FindAllString(strings.ToLower(remaining), -1)

	seen := make(map[string]bool, len(matches)+len(phrases))
	var tokens []string

	for _, phrase := range phrases {
		if !seen[phrase] {
			seen[phrase] = true
			tokens = append(tokens, phrase)
		}
	}

	for _, token := range matches {
		if seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}

	if len(tokens) == 0 {
		return "", ""
	}

	sort.Strings(tokens)
	orQuery = strings.Join(tokens, " OR ")
	if len(tokens) == 1 {
		return orQuery, orQuery
	}
	andQuery = strings.Join(tokens, " AND ")
	return andQuery, orQuery
}
