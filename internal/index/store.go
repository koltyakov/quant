package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		hnsw:                      newHNSWIndex(),
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
		key := ChunkDiffKey(cr.Content)
		result[key] = cr
	}
	return result, rows.Err()
}

func ChunkDiffKey(content string) string {
	h := sha256.Sum256([]byte(content))
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
			s.hnsw.mu.Unlock()
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM documents`)
		return err
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
