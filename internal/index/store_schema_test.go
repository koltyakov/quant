package index

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrate_FullSchemaCreation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var tableCount int
	err = store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('documents','chunks','embedding_metadata','hnsw_state','quarantine','content_dedup')`,
	).Scan(&tableCount)
	if err != nil {
		t.Fatalf("counting tables: %v", err)
	}
	if tableCount != 6 {
		t.Fatalf("expected 6 tables, got %d", tableCount)
	}

	var ftsCount int
	err = store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'chunks_%'`,
	).Scan(&ftsCount)
	if err != nil {
		t.Fatalf("counting triggers: %v", err)
	}
	if ftsCount != 3 {
		t.Fatalf("expected 3 FTS triggers, got %d", ftsCount)
	}
}

func TestResetIndex_ClearsHNSWState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_, err = store.db.ExecContext(ctx, `INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions) VALUES (1, CURRENT_TIMESTAMP, 5, 'test-model', 128)`)
	if err != nil {
		t.Fatalf("inserting hnsw_state: %v", err)
	}

	if err := store.resetIndex(ctx); err != nil {
		t.Fatalf("resetIndex() error: %v", err)
	}

	var count int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hnsw_state`).Scan(&count)
	if err != nil {
		t.Fatalf("querying hnsw_state: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 hnsw_state rows after reset, got %d", count)
	}
}

func TestResetIndex_ClearsFTS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{Path: "fts_reset/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "fts reset test content", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}

	if err := store.resetIndex(ctx); err != nil {
		t.Fatalf("resetIndex() error: %v", err)
	}

	var logicalRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts`).Scan(&logicalRows); err != nil {
		t.Fatalf("querying chunks_fts: %v", err)
	}
	if logicalRows != 0 {
		t.Fatalf("expected 0 FTS rows after reset, got %d", logicalRows)
	}
}

func TestUpsertDocumentTx_InsertAndUpdate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	doc := &Document{Path: "tx/doc.txt", Hash: "h1", ModifiedAt: time.Now(), FileType: "txt", Language: "en"}
	id, err := upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		t.Fatalf("upsertDocumentTx insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	doc.Hash = "h2"
	doc.FileType = "md"
	id2, err := upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		t.Fatalf("upsertDocumentTx update: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected same id on update, got %d vs %d", id, id2)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := store.GetDocumentByPath(ctx, "tx/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	if got.Hash != "h2" || got.FileType != "md" {
		t.Fatalf("expected updated doc, got hash=%q file_type=%q", got.Hash, got.FileType)
	}
}

func TestUpsertDocumentTx_WithTags(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	doc := &Document{
		Path:       "tags/tx.txt",
		Hash:       "ht",
		ModifiedAt: time.Now(),
		Tags:       map[string]string{"env": "prod"},
	}
	_, err = upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		t.Fatalf("upsertDocumentTx with tags: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestDeleteChunksByDocumentIDTx(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{Path: "delchunk/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: id, Content: fmt.Sprintf("chunk %d", i), ChunkIndex: i, Embedding: EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("InsertChunk(%d): %v", i, err)
		}
	}

	_, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats before: %v", err)
	}
	if chunkCount != 3 {
		t.Fatalf("expected 3 chunks before delete, got %d", chunkCount)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteChunksByDocumentIDTx(ctx, tx, id); err != nil {
		t.Fatalf("deleteChunksByDocumentIDTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, chunkCount, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("expected 0 chunks after delete, got %d", chunkCount)
	}
}

func TestRebuildChunksFTSTx(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{Path: "fts_rebuild/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "rebuild test", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := rebuildChunksFTSTx(ctx, tx); err != nil {
		t.Fatalf("rebuildChunksFTSTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestClearHNSWStateTx(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_, err = store.db.ExecContext(ctx, `INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions) VALUES (1, CURRENT_TIMESTAMP, 10, 'm', 64)`)
	if err != nil {
		t.Fatalf("insert hnsw_state: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := clearHNSWStateTx(ctx, tx); err != nil {
		t.Fatalf("clearHNSWStateTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hnsw_state`).Scan(&count); err != nil {
		t.Fatalf("count hnsw_state: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 hnsw_state rows, got %d", count)
	}
}

func TestMigrateHNSWStateColumns_OldSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE hnsw_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		built_at DATETIME NOT NULL,
		node_count INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old hnsw_state: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE embedding_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create embedding_metadata: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, hash TEXT NOT NULL,
		modified_at DATETIME NOT NULL, indexed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		file_type TEXT NOT NULL DEFAULT '', language TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
		tags TEXT NOT NULL DEFAULT '', collection TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create documents: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content TEXT NOT NULL, chunk_index INTEGER NOT NULL, embedding BLOB NOT NULL,
		parent_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL,
		depth INTEGER NOT NULL DEFAULT 0, section_title TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create chunks: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() on old schema: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_, err = store.db.ExecContext(ctx, `INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions) VALUES (1, CURRENT_TIMESTAMP, 3, 'model-x', 32)`)
	if err != nil {
		t.Fatalf("insert into migrated hnsw_state: %v", err)
	}
}

func TestMigrateDocumentMetadata_OldSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, hash TEXT NOT NULL,
		modified_at DATETIME NOT NULL, indexed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create old documents: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content TEXT NOT NULL, chunk_index INTEGER NOT NULL, embedding BLOB NOT NULL,
		parent_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL,
		depth INTEGER NOT NULL DEFAULT 0, section_title TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create chunks: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE embedding_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create embedding_metadata: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE hnsw_state (
		id INTEGER PRIMARY KEY CHECK (id = 1), built_at DATETIME NOT NULL, node_count INTEGER NOT NULL,
		model TEXT NOT NULL DEFAULT '', dimensions INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create hnsw_state: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE quarantine (path TEXT PRIMARY KEY, error_msg TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, attempts INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		t.Fatalf("create quarantine: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE content_dedup (content_hash TEXT PRIMARY KEY, embedding BLOB NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("create content_dedup: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() on old schema: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path: "oldmeta/doc.txt", Hash: "h1", ModifiedAt: time.Now(),
		FileType: "txt", Language: "text", Title: "Old Meta Doc",
	})
	if err != nil {
		t.Fatalf("UpsertDocument after metadata migration: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "oldmeta/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	if doc == nil || doc.FileType != "txt" || doc.Language != "text" || doc.Title != "Old Meta Doc" {
		t.Fatalf("unexpected doc metadata: %+v", doc)
	}
}

func TestVacuum_AfterReset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := store.UpsertDocument(ctx, &Document{
			Path: fmt.Sprintf("vac_reset/doc%d.txt", i), Hash: fmt.Sprintf("h%d", i), ModifiedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("UpsertDocument: %v", err)
		}
	}

	if err := store.resetIndex(ctx); err != nil {
		t.Fatalf("resetIndex: %v", err)
	}

	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum after reset: %v", err)
	}
}

func TestMigrate_HierarchicalChunksMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, hash TEXT NOT NULL,
		modified_at DATETIME NOT NULL, indexed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		file_type TEXT NOT NULL DEFAULT '', language TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
		tags TEXT NOT NULL DEFAULT '', collection TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create documents: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content TEXT NOT NULL, chunk_index INTEGER NOT NULL, embedding BLOB NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old chunks: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE embedding_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create embedding_metadata: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE hnsw_state (
		id INTEGER PRIMARY KEY CHECK (id = 1), built_at DATETIME NOT NULL, node_count INTEGER NOT NULL,
		model TEXT NOT NULL DEFAULT '', dimensions INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("create hnsw_state: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() on old schema without hierarchical columns: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{Path: "hier/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID, Content: "hierarchical chunk", ChunkIndex: 0,
		Embedding: EncodeFloat32([]float32{1}), ParentID: nil, Depth: 0, SectionTitle: "Intro",
	}); err != nil {
		t.Fatalf("InsertChunk with hierarchical fields: %v", err)
	}
}

func TestMigrate_SummaryColumnMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{Path: "summary/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID, Content: "summary chunk", ChunkIndex: 0,
		Embedding: EncodeFloat32([]float32{1}), Summary: "a summary",
	}); err != nil {
		t.Fatalf("InsertChunk with summary: %v", err)
	}
}

func TestMigrate_CollectionColumnMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path: "col_migrate/doc.txt", Hash: "h1", ModifiedAt: time.Now(), Collection: "test-col",
	})
	if err != nil {
		t.Fatalf("UpsertDocument with collection: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "col_migrate/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	if doc == nil || doc.Collection != "test-col" {
		t.Fatalf("expected collection 'test-col', got %q", doc.Collection)
	}
}

func TestEnsureEmbeddingMetadata_ResetOnPopulatedEmptyStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{Path: "empty_meta/doc.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "chunk", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}

	needsReset, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 1, Normalized: true})
	if err != nil {
		t.Fatalf("EnsureEmbeddingMetadata: %v", err)
	}
	if !needsReset {
		t.Fatal("expected reset for populated DB with no metadata")
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected empty store after reset, got %d docs, %d chunks", docCount, chunkCount)
	}
}
