package index

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
	_ "modernc.org/sqlite"
)

type Store struct {
	db                        *sql.DB
	dbPath                    string
	backup                    string // non-empty if a pre-existing DB was backed up due to migration failure
	maxVectorSearchCandidates int
	hnsw                      *hnswIndex
}

const defaultMaxVectorSearchCandidates = 20000

const (
	defaultSQLiteConnMaxLifetime = time.Hour
	defaultSQLiteConnMaxIdleTime = 15 * time.Minute
)

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
	logx.Warn("migration failed; backing up existing database", "backup_path", backupPath, "err", err)

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
	logx.Info("removed database backup", "path", s.backup)
	s.backup = ""
}

func openStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
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
	db.SetConnMaxLifetime(defaultSQLiteConnMaxLifetime)
	db.SetConnMaxIdleTime(defaultSQLiteConnMaxIdleTime)

	s := &Store{
		db:                        db,
		dbPath:                    dbPath,
		maxVectorSearchCandidates: defaultMaxVectorSearchCandidates,
		hnsw:                      newHNSWIndex(dbPath),
	}
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
	var err error
	if s != nil && s.db != nil {
		s.FlushHNSWNow()
		if _, checkpointErr := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); checkpointErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpointing sqlite wal: %w", checkpointErr))
		}
		err = errors.Join(err, s.db.Close())
	}
	return err
}

func (s *Store) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) SetMaxVectorSearchCandidates(max int) {
	s.maxVectorSearchCandidates = max
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

	// Remove existing chunks from HNSW inside the transaction so we have
	// deterministic cleanup even under concurrent calls for the same path.
	var hnswDeleteIDs []int
	if s.hnsw != nil && s.hnsw.ready.Load() {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, docID)
		if err == nil {
			for rows.Next() {
				var id int
				if rows.Scan(&id) == nil {
					hnswDeleteIDs = append(hnswDeleteIDs, id)
				}
			}
			_ = rows.Close()
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("deleting existing chunks: %w", err)
	}

	for _, id := range hnswDeleteIDs {
		s.hnsw.Delete(id)
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks (document_id, content, chunk_index, embedding) VALUES (?, ?, ?, ?) RETURNING id`)
	if err != nil {
		return fmt.Errorf("preparing chunk insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	type insertedChunk struct {
		id        int
		embedding []byte
	}
	inserted := make([]insertedChunk, 0, len(chunks))

	for _, chunk := range chunks {
		var newID int
		if err := stmt.QueryRowContext(ctx,
			docID, chunk.Content, chunk.ChunkIndex, chunk.Embedding,
		).Scan(&newID); err != nil {
			return fmt.Errorf("inserting chunk %d: %w", chunk.ChunkIndex, err)
		}
		inserted = append(inserted, insertedChunk{id: newID, embedding: chunk.Embedding})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	// Add newly inserted chunks to HNSW after commit.
	if s.hnsw != nil && s.hnsw.ready.Load() {
		meta, metaErr := s.embeddingMetadata(ctx)
		if metaErr != nil {
			logx.Warn("failed to read embedding metadata for HNSW update", "err", metaErr)
		} else if meta != nil && meta.Dimensions > 0 {
			for _, ic := range inserted {
				vec := decodeEmbeddingForHNSW(ic.embedding, meta.Dimensions)
				if len(vec) > 0 {
					s.hnswAdd(ic.id, vec)
				}
			}
		}
	}

	s.FlushHNSW()

	return nil
}

// GetDocumentChunksByPath returns all existing chunks for the document at path,
// keyed by a compound of content and chunk index. Used for incremental reindex diffing.
func (s *Store) GetDocumentChunksByPath(ctx context.Context, path string) (map[string]ChunkRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.chunk_index, c.content, c.embedding
		 FROM chunks c
		 JOIN documents d ON c.document_id = d.id
		 WHERE d.path = ?`,
		path,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]ChunkRecord)
	for rows.Next() {
		var cr ChunkRecord
		if err := rows.Scan(&cr.ID, &cr.ChunkIndex, &cr.Content, &cr.Embedding); err != nil {
			return nil, err
		}
		key := ChunkDiffKey(cr.Content, cr.ChunkIndex)
		result[key] = cr
	}
	return result, rows.Err()
}

func ChunkDiffKey(content string, chunkIndex int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", chunkIndex, content)))
	return fmt.Sprintf("%x", h[:8])
}

func (s *Store) DeleteChunksByDocument(ctx context.Context, docID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID)
	return err
}

func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	var hnswDeleteIDs []int
	if s.hnsw != nil && s.hnsw.ready.Load() {
		doc, err := s.GetDocumentByPath(ctx, path)
		if err == nil && doc != nil {
			rows, err := s.db.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, doc.ID)
			if err == nil {
				for rows.Next() {
					var id int
					if rows.Scan(&id) == nil {
						hnswDeleteIDs = append(hnswDeleteIDs, id)
					}
				}
				_ = rows.Close()
			}
		}
	}

	_, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE path = ?`, path)
	if err != nil {
		return err
	}

	for _, id := range hnswDeleteIDs {
		s.hnsw.Delete(id)
	}

	s.FlushHNSW()
	return nil
}

func (s *Store) DeleteDocumentsByPrefix(ctx context.Context, prefix string) error {
	prefix = filepath.Clean(prefix)
	if prefix == "." || prefix == "" {
		if s.hnsw != nil && s.hnsw.ready.Load() {
			s.hnsw.mu.Lock()
			s.hnsw.graph = hnsw.NewGraph[int]()
			s.hnsw.graph.M = hnswM
			s.hnsw.graph.EfSearch = hnswEfSearch
			s.hnsw.dirty.Store(true)
			s.hnsw.mu.Unlock()
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM documents`)
		if err != nil {
			return err
		}
		s.FlushHNSWNow()
		return nil
	}

	if s.hnsw != nil && s.hnsw.ready.Load() {
		rows, err := s.db.QueryContext(ctx,
			`SELECT DISTINCT c.document_id FROM chunks c JOIN documents d ON c.document_id = d.id WHERE d.path = ? OR d.path LIKE ? ESCAPE '\'`,
			prefix, sqlLikePrefixPattern(prefix),
		)
		if err == nil {
			for rows.Next() {
				var docID int64
				if rows.Scan(&docID) == nil {
					s.hnswDeleteChunks(ctx, docID)
				}
			}
			_ = rows.Close()
		}
	}

	likePrefix := prefix + string(filepath.Separator) + "%"
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM documents WHERE path = ? OR path LIKE ?`,
		prefix, likePrefix,
	)
	if err != nil {
		return err
	}
	s.FlushHNSW()
	return nil
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
	return s.ListDocumentsLimit(ctx, 0)
}

func (s *Store) ListDocumentsLimit(ctx context.Context, limit int) ([]Document, error) {
	return s.listDocuments(ctx, limit)
}

func (s *Store) listDocuments(ctx context.Context, limit int) ([]Document, error) {
	query := `SELECT id, path, hash, modified_at, indexed_at FROM documents ORDER BY path`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	docs := make([]Document, 0, min(limit, 256))
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

	andQuery, orQuery, nearQuery := buildFTSQueries(query)
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
		collected, err := s.collectFTSCandidates(ctx, orQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
		rankOffset += collected
	}

	// NEAR pass: boost candidates where query terms appear within 10 tokens of each other.
	if nearQuery != "" {
		_, err := s.collectFTSCandidates(ctx, nearQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
	}

	keywordResults := rerankKeywordCandidates(keywordCandidates, limit, pathQueryTokens(query))
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

// searchVector performs vector similarity search. It uses the HNSW approximate
// nearest-neighbor index when available, falling back to brute-force scan for small
// corpora or when the index is not yet ready.
func (s *Store) searchVector(ctx context.Context, queryEmbedding []float32, limit int, pathPrefix string, exclude map[int]*searchCandidate) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	// Prefer HNSW when ready.
	if s.hnsw != nil && s.hnsw.ready.Load() {
		if pathPrefix == "" {
			return s.searchVectorHNSW(ctx, queryEmbedding, limit, exclude)
		}
		return s.searchVectorHNSWWithPrefix(ctx, queryEmbedding, limit, pathPrefix, exclude)
	}

	if ok, err := s.canRunVectorFallback(ctx, pathPrefix); err != nil {
		return nil, err
	} else if !ok {
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

// pathQueryTokens extracts lowercased path-segment tokens from the raw query for
// path-match boosting. It uses the raw token pattern (no stemming, no expansion) so that
// identifiers like "auth" match path segments like "auth/middleware.go" exactly.
func pathQueryTokens(query string) []string {
	matches := ftsTokenPattern.FindAllString(strings.ToLower(query), -1)
	seen := make(map[string]bool, len(matches))
	var tokens []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			tokens = append(tokens, m)
		}
	}
	return tokens
}

// rerankKeywordCandidates combines keyword rank with vector similarity rank
// using Reciprocal Rank Fusion: score = 1/(k+keywordRank) + 1/(k+vectorRank), k=60.
// pathTokens are raw query tokens checked against the document path for a rank bonus.
func rerankKeywordCandidates(candidates map[int]*searchCandidate, limit int, pathTokens []string) []SearchResult {
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
		// Path-match bonus: add a third RRF term if any query token appears in the doc path.
		if len(pathTokens) > 0 {
			lowerPath := strings.ToLower(candidate.result.DocumentPath)
			for _, tok := range pathTokens {
				if strings.Contains(lowerPath, tok) {
					rrf += 1.0 / float32(k+1)
					break
				}
			}
		}
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

// EncodeInt8 quantizes a float32 vector to uint8 with per-vector min/max scaling.
// Format: 4 bytes min (float32 LE) + 4 bytes scale (float32 LE) + len(vec) bytes uint8.
// Storage is 4x smaller than float32 (8 byte header + 1 byte/dim vs 4 bytes/dim).
// Quality loss is <1% on recall@10 for L2-normalized embeddings.
func EncodeInt8(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	minVal, maxVal := vec[0], vec[0]
	for _, v := range vec[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	var scale float32
	if maxVal > minVal {
		scale = (maxVal - minVal) / 255.0
	}

	buf := make([]byte, 8+len(vec))
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(minVal))
	binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(scale))
	for i, v := range vec {
		var q uint8
		if scale > 0 {
			qf := (v - minVal) / scale
			if qf < 0 {
				qf = 0
			} else if qf > 255 {
				qf = 255
			}
			q = uint8(math.Round(float64(qf)))
		}
		buf[8+i] = q
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
	switch len(encoded) {
	case len(query) * 4:
		// float32 format.
		var dot float32
		for i, q := range query {
			v := math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
			dot += q * v
		}
		return dot
	case 8 + len(query):
		// int8 quantized format: 4-byte min + 4-byte scale + uint8 per dim.
		minVal := math.Float32frombits(binary.LittleEndian.Uint32(encoded[0:]))
		scale := math.Float32frombits(binary.LittleEndian.Uint32(encoded[4:]))
		var dot float32
		for i, q := range query {
			v := float32(encoded[8+i])*scale + minVal
			dot += q * v
		}
		return dot
	default:
		return 0
	}
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

var ftsOperatorPattern = regexp.MustCompile(`\b(AND|OR|NOT|NEAR)\b`)

func ftsSanitizePhrase(phrase string) string {
	return ftsOperatorPattern.ReplaceAllString(phrase, "")
}

// camelCasePattern matches camelCase identifiers (at least one lowercase letter followed by an uppercase).
var camelCasePattern = regexp.MustCompile(`[a-z][A-Z]`)

// splitIdentifier expands a camelCase or snake_case identifier into its component words.
// Returns nil if the token is neither camelCase nor snake_case (no expansion needed).
func splitIdentifier(token string) []string {
	isCamel := camelCasePattern.MatchString(token)
	isSnake := strings.Contains(token, "_") && token != "_"
	if !isCamel && !isSnake {
		return nil
	}

	var words []string
	if isSnake {
		for _, part := range strings.Split(token, "_") {
			if part != "" {
				words = append(words, strings.ToLower(part))
			}
		}
	} else {
		// camelCase: split before each uppercase letter that follows a lowercase.
		var current []rune
		runes := []rune(token)
		for i, r := range runes {
			if i > 0 && r >= 'A' && r <= 'Z' && runes[i-1] >= 'a' && runes[i-1] <= 'z' {
				if len(current) > 0 {
					words = append(words, strings.ToLower(string(current)))
				}
				current = []rune{r}
			} else {
				current = append(current, r)
			}
		}
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
		}
	}
	if len(words) <= 1 {
		return nil
	}
	return words
}

// buildFTSQueries converts a natural-language query into AND, OR, and NEAR FTS5 queries.
// The AND query requires all terms to match (tighter); the OR query matches any term.
// The NEAR query (non-empty for 2-4 raw tokens) adds a proximity bonus within 10 tokens.
// Identifier expansion: camelCase and snake_case tokens are expanded so that
// "getUserName" also matches chunks containing the individual words.
// Prefix matching: the last bare token gets a "*" suffix for autocomplete-style matching.
func buildFTSQueries(query string) (andQuery, orQuery, nearQuery string) {
	// Extract quoted phrases first and sanitize them for FTS5.
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
			phrase = ftsSanitizePhrase(phrase)
			phrases = append(phrases, `"`+phrase+`"`)
		}
		remaining = remaining[:start] + " " + remaining[start+1+end+1:]
	}

	// Tokenize the remaining (non-phrase) text.
	// Extract original-case tokens for identifier detection, then lowercase for FTS.
	originalMatches := ftsTokenPattern.FindAllString(remaining, -1)
	rawMatches := make([]string, len(originalMatches))
	for i, m := range originalMatches {
		rawMatches[i] = strings.ToLower(m)
	}

	seen := make(map[string]bool, len(rawMatches)+len(phrases))
	var tokens []string
	// originalByLower maps lowercased token back to original case for identifier detection.
	originalByLower := make(map[string]string, len(rawMatches))
	for i, token := range rawMatches {
		originalByLower[token] = originalMatches[i]
	}

	for _, phrase := range phrases {
		if !seen[phrase] {
			seen[phrase] = true
			tokens = append(tokens, phrase)
		}
	}

	for _, token := range rawMatches {
		if seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}

	if len(tokens) == 0 {
		return "", "", ""
	}

	// NEAR query: built from raw bare tokens only (no phrases, no expanded tokens).
	// Only valid when there are 2-4 simple terms; FTS5 NEAR() does not support phrases.
	var bareTokens []string
	for _, t := range tokens {
		if t[0] != '"' {
			bareTokens = append(bareTokens, t)
		}
	}
	if len(bareTokens) >= 2 && len(bareTokens) <= 4 {
		nearQuery = "NEAR(" + strings.Join(bareTokens, " ") + ", 10)"
	}

	// Prefix matching: add lastToken* variant for the last bare token.
	lastToken := tokens[len(tokens)-1]
	if len(lastToken) > 0 && lastToken[0] != '"' {
		prefixToken := lastToken + "*"
		if !seen[prefixToken] {
			seen[prefixToken] = true
			tokens = append(tokens, prefixToken)
		}
	}

	// Identifier expansion: for each camelCase/snake_case token, add its component
	// words as an OR expansion. Keep the original as a quoted phrase for exact matches.
	var expandedTokens []string
	for _, token := range tokens {
		expandedTokens = append(expandedTokens, token)
		if token[0] == '"' {
			continue
		}
		// Strip trailing * before checking for identifier expansion.
		// Use the original case (if available) for camelCase detection.
		bare := strings.TrimSuffix(token, "*")
		originalBare := originalByLower[bare]
		if originalBare == "" {
			originalBare = bare
		}
		parts := splitIdentifier(originalBare)
		if len(parts) == 0 {
			continue
		}
		// Add the original as a quoted phrase for exact match.
		quoted := `"` + bare + `"`
		if !seen[quoted] {
			seen[quoted] = true
			expandedTokens = append(expandedTokens, quoted)
		}
		// Add each component word.
		for _, part := range parts {
			if !seen[part] {
				seen[part] = true
				expandedTokens = append(expandedTokens, part)
			}
		}
	}
	tokens = expandedTokens

	sort.Strings(tokens)
	orQuery = strings.Join(tokens, " OR ")
	if len(tokens) == 1 {
		return orQuery, orQuery, nearQuery
	}
	andQuery = strings.Join(tokens, " AND ")
	return andQuery, orQuery, nearQuery
}
