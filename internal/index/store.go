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
	backup                    string
	maxVectorSearchCandidates int
	hnsw                      *hnswIndex
	hnswM                     int
	hnswEfSearch              int
	hnswGraphPath             string
	keywordWeightOverride     float32
	vectorWeightOverride      float32
	docEmbeds                 *docEmbeddingIndex
}

const defaultMaxVectorSearchCandidates = 20000

const (
	defaultHNSWM        = 16
	defaultHNSWEfSearch = 100
)

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
		hnswM:                     defaultHNSWM,
		hnswEfSearch:              defaultHNSWEfSearch,
		hnswGraphPath:             dbPath + ".hnsw",
		docEmbeds:                 newDocEmbeddingIndex(),
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
	if err := s.cleanupOrphanedChunks(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cleaning orphaned chunks: %w", err)
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

func (s *Store) SetHNSWParams(m, efSearch int) {
	if m > 0 {
		s.hnswM = m
	}
	if efSearch > 0 {
		s.hnswEfSearch = efSearch
	}
}

func (s *Store) HNSWReoptimizationNeeded(threshold float64) bool {
	if s.hnsw == nil || !s.hnsw.ready.Load() {
		return false
	}
	total := s.hnsw.Len()
	if total == 0 {
		return false
	}
	return float64(s.hnsw.modCount())/float64(total) > threshold
}

func (s *Store) SetWeightOverrides(keyword, vector float32) {
	s.keywordWeightOverride = keyword
	s.vectorWeightOverride = vector
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
	CREATE TABLE IF NOT EXISTS hnsw_state (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		built_at    DATETIME NOT NULL,
		node_count  INTEGER NOT NULL,
		model       TEXT NOT NULL DEFAULT '',
		dimensions  INTEGER NOT NULL DEFAULT 0
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
	if err != nil {
		return err
	}
	if err := s.migrateHNSWStateColumns(); err != nil {
		return err
	}
	return s.migrateDocEmbeddingColumn()
}

func (s *Store) migrateHNSWStateColumns() error {
	var modelCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('hnsw_state') WHERE name='model'`,
	).Scan(&modelCount)
	if err != nil {
		return fmt.Errorf("checking hnsw_state schema: %w", err)
	}
	if modelCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE hnsw_state ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding hnsw_state.model column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE hnsw_state ADD COLUMN dimensions INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("adding hnsw_state.dimensions column: %w", err)
		}
	}
	return nil
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
	return s.ReindexDocumentWithDeferredHNSW(ctx, doc, chunks, nil)
}

func (s *Store) ReindexDocumentWithDeferredHNSW(ctx context.Context, doc *Document, chunks []ChunkRecord, deferredHNSW func()) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	docID, err := upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		return err
	}

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

	if len(hnswDeleteIDs) > 0 {
		s.hnsw.BatchDelete(hnswDeleteIDs)
	}

	meta, _ := s.embeddingMetadata(ctx)
	dims := 0
	if meta != nil {
		dims = meta.Dimensions
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

	if dims > 0 && len(chunks) > 0 {
		docEmb := computeDocEmbedding(chunks, dims)
		if docEmb != nil {
			if err := s.updateDocEmbeddingTx(ctx, tx, docID, docEmb); err != nil {
				logx.Warn("failed to store document embedding", "doc_id", docID, "err", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	if deferredHNSW != nil {
		deferredHNSW()
	}

	if s.hnsw != nil && s.hnsw.ready.Load() {
		meta2, metaErr := s.embeddingMetadata(ctx)
		if metaErr != nil {
			logx.Warn("failed to read embedding metadata for HNSW update", "err", metaErr)
		} else if meta2 != nil && meta2.Dimensions > 0 {
			var nodes []hnsw.Node[int]
			for _, ic := range inserted {
				vec := decodeEmbeddingForHNSW(ic.embedding, meta2.Dimensions)
				if len(vec) > 0 {
					nodes = append(nodes, hnsw.MakeNode(ic.id, vec))
				}
			}
			s.hnsw.BatchAdd(nodes)
		}
	}

	if dims > 0 && len(chunks) > 0 {
		docEmb := computeDocEmbedding(chunks, dims)
		if docEmb != nil {
			vec := decodeEmbeddingForHNSW(docEmb, dims)
			if len(vec) > 0 {
				s.docEmbeds.Set(docID, doc.Path, NormalizeFloat32(vec))
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
	return fmt.Sprintf("%x", h[:])
}

func (s *Store) DeleteChunksByDocument(ctx context.Context, docID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID)
	return err
}

func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	var hnswDeleteIDs []int
	var docID int64
	if s.hnsw != nil && s.hnsw.ready.Load() {
		doc, err := s.GetDocumentByPath(ctx, path)
		if err == nil && doc != nil {
			docID = doc.ID
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
	if docID == 0 {
		doc, err := s.GetDocumentByPath(ctx, path)
		if err != nil {
			return err
		}
		if doc == nil {
			return nil
		}
		docID = doc.ID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteChunksByDocumentIDTx(ctx, tx, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, docID); err != nil {
		return fmt.Errorf("deleting document: %w", err)
	}
	if err := rebuildChunksFTSTx(ctx, tx); err != nil {
		return err
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete transaction: %w", err)
	}

	if len(hnswDeleteIDs) > 0 {
		s.hnsw.BatchDelete(hnswDeleteIDs)
	}

	s.docEmbeds.Remove(docID, path)

	return nil
}

func (s *Store) DeleteDocumentsByPrefix(ctx context.Context, prefix string) error {
	prefix = filepath.Clean(prefix)
	if prefix == "." || prefix == "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning delete-all transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
			return fmt.Errorf("clearing chunks: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM documents`); err != nil {
			return fmt.Errorf("clearing documents: %w", err)
		}
		if err := rebuildChunksFTSTx(ctx, tx); err != nil {
			return err
		}
		if err := clearHNSWStateTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing delete-all transaction: %w", err)
		}

		if s.hnsw != nil && s.hnsw.ready.Load() {
			s.hnsw.mu.Lock()
			s.hnsw.graph = newGraph(s.hnswM, s.hnswEfSearch)
			s.hnsw.mu.Unlock()
		}
		return nil
	}

	var hnswDeleteIDs []int
	if s.hnsw != nil && s.hnsw.ready.Load() {
		rows, err := s.db.QueryContext(ctx,
			`SELECT c.id
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id
			 WHERE d.path = ? OR d.path LIKE ? ESCAPE '\'`,
			prefix, sqlLikePrefixPattern(prefix),
		)
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

	likePrefix := prefix + string(filepath.Separator) + "%"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning delete-by-prefix transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks
		 WHERE document_id IN (
		 	SELECT id FROM documents WHERE path = ? OR path LIKE ? ESCAPE '\'
		 )`,
		prefix, likePrefix,
	); err != nil {
		return fmt.Errorf("deleting chunks by prefix: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM documents WHERE path = ? OR path LIKE ? ESCAPE '\'`,
		prefix, likePrefix,
	); err != nil {
		return fmt.Errorf("deleting documents by prefix: %w", err)
	}
	if err := rebuildChunksFTSTx(ctx, tx); err != nil {
		return err
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete-by-prefix transaction: %w", err)
	}

	if len(hnswDeleteIDs) > 0 {
		s.hnsw.BatchDelete(hnswDeleteIDs)
	}
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

func (s *Store) FTSDiagnostics(ctx context.Context) (FTSDiagnostics, error) {
	var diag FTSDiagnostics

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts`).Scan(&diag.LogicalRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts rows: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts_data`).Scan(&diag.DataRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts_data rows: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts_idx`).Scan(&diag.IdxRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts_idx rows: %w", err)
	}

	diag.Empty = diag.LogicalRows == 0
	return diag, nil
}
