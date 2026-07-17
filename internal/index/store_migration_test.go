package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func createMarkedIncompatibleDatabase(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open incompatible database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE sentinel (value TEXT)`); err != nil {
		t.Fatalf("create sentinel table: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA application_id = %d`, quantApplicationID)); err != nil {
		t.Fatalf("set quant application id: %v", err)
	}
	if _, err := db.Exec(`CREATE VIEW documents AS SELECT 1 AS id`); err != nil {
		t.Fatalf("create incompatible schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close incompatible database: %v", err)
	}
}

func createLegacyFTSSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE chunks_fts USING fts5(
			content,
			content='chunks',
			content_rowid='id'
		);
		CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
			INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
		END;
		CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
		END;
		CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
			INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
			INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
		END;
	`); err != nil {
		t.Fatalf("create legacy FTS schema: %v", err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate() error: %v", err)
	}
}

func TestMigrate_OldSchemaToNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open error: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL UNIQUE,
		hash TEXT NOT NULL,
		modified_at DATETIME NOT NULL,
		indexed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create old documents table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		embedding BLOB NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old chunks table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE embedding_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create embedding_metadata table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE hnsw_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		built_at DATETIME NOT NULL,
		node_count INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old hnsw_state table: %v", err)
	}
	createLegacyFTSSchema(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close old db: %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() on old schema error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "migrate/old.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
		Collection: "testcol",
		FileType:   "txt",
		Language:   "text",
	})
	if err != nil {
		t.Fatalf("UpsertDocument after migration error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "migrate/old.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.Collection != "testcol" {
		t.Fatalf("expected collection 'testcol', got %q", doc.Collection)
	}
	var applicationID int64
	if err := store.db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		t.Fatalf("read migrated application id: %v", err)
	}
	if applicationID != quantApplicationID {
		t.Fatalf("migrated application_id = %d, want %d", applicationID, quantApplicationID)
	}
}

func TestNewStore_DoesNotRecoverArbitraryOpenFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	if err := os.Mkdir(dbPath, 0750); err != nil {
		t.Fatalf("Mkdir(database path): %v", err)
	}

	store, err := NewStore(dbPath)
	if err == nil {
		_ = store.Close()
		t.Fatal("NewStore() unexpectedly succeeded for a directory path")
	}
	info, statErr := os.Stat(dbPath)
	if statErr != nil {
		t.Fatalf("database path was removed: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("database path was destructively replaced: mode=%v", info.Mode())
	}
	if _, statErr := os.Stat(dbPath + ".bak"); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected backup after non-recoverable open failure: %v", statErr)
	}
}

func TestNewStore_RecoversSchemaIncompatibility(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	createMarkedIncompatibleDatabase(t, dbPath)

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() recovery error: %v", err)
	}
	mustCloseStore(t, store)
	if store.backup != dbPath+".bak" {
		t.Fatalf("backup path = %q, want %q", store.backup, dbPath+".bak")
	}
	if _, err := os.Stat(store.backup); err != nil {
		t.Fatalf("stat recovered backup: %v", err)
	}
	if _, _, err := store.Stats(context.Background()); err != nil {
		t.Fatalf("fresh store Stats() error: %v", err)
	}
}

func TestNewStore_RecoversDatabaseSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	createMarkedIncompatibleDatabase(t, target)
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	store, err := NewStore(alias)
	if err != nil {
		t.Fatalf("NewStore() recovery error: %v", err)
	}
	defer func() { _ = store.Close() }()
	if store.dbPath != resolved {
		t.Fatalf("store db path = %q, want resolved target %q", store.dbPath, resolved)
	}
	if store.backup != resolved+".bak" {
		t.Fatalf("backup path = %q, want %q", store.backup, resolved+".bak")
	}
	info, err := os.Lstat(alias)
	if err != nil {
		t.Fatalf("Lstat database alias: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("database alias was replaced: mode=%v", info.Mode())
	}
	if _, _, err := store.Stats(context.Background()); err != nil {
		t.Fatalf("recovered store Stats() error: %v", err)
	}
}

func TestNewStore_ReturnsBackupRemovalError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	createMarkedIncompatibleDatabase(t, dbPath)
	if err := os.Mkdir(dbPath+".bak", 0750); err != nil {
		t.Fatalf("Mkdir(stale backup): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbPath+".bak", "keep"), []byte("keep"), 0600); err != nil {
		t.Fatalf("WriteFile(stale backup content): %v", err)
	}

	store, err := NewStore(dbPath)
	if err == nil {
		_ = store.Close()
		t.Fatal("NewStore() unexpectedly ignored stale backup removal failure")
	}
	if !strings.Contains(err.Error(), "removing stale backup") {
		t.Fatalf("NewStore() error = %v, want stale backup removal context", err)
	}
	db, openErr := sql.Open("sqlite", dbPath)
	if openErr != nil {
		t.Fatalf("reopen original database: %v", openErr)
	}
	defer func() { _ = db.Close() }()
	var sentinelCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sentinel`).Scan(&sentinelCount); err != nil {
		t.Fatalf("original marked database changed: %v", err)
	}
}

func TestNewStore_RejectsNonSQLiteWithoutModification(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.txt")
	original := []byte("important user data")
	if err := os.WriteFile(dbPath, original, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := NewStore(dbPath)
	if store != nil {
		_ = store.Close()
		t.Fatal("NewStore unexpectedly opened a non-SQLite file")
	}
	if !errors.Is(err, ErrNotQuantDatabase) {
		t.Fatalf("NewStore error = %v, want ErrNotQuantDatabase", err)
	}
	got, readErr := os.ReadFile(dbPath)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("file contents changed: got %q, want %q", got, original)
	}
	if _, statErr := os.Stat(dbPath + ".bak"); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected backup for rejected file: %v", statErr)
	}
}

func TestNewStore_RejectsUnrelatedSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE user_data (value TEXT); INSERT INTO user_data VALUES ('keep')`); err != nil {
		t.Fatalf("seed unrelated database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unrelated database: %v", err)
	}

	store, err := NewStore(dbPath)
	if store != nil {
		_ = store.Close()
		t.Fatal("NewStore unexpectedly opened an unrelated SQLite database")
	}
	if !errors.Is(err, ErrNotQuantDatabase) {
		t.Fatalf("NewStore error = %v, want ErrNotQuantDatabase", err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen unrelated database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRow(`SELECT value FROM user_data`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("unrelated database changed: value=%q err=%v", value, err)
	}
	var quantTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('documents', 'chunks')`).Scan(&quantTables); err != nil {
		t.Fatalf("inspect unrelated database: %v", err)
	}
	if quantTables != 0 {
		t.Fatalf("unrelated database gained %d quant tables", quantTables)
	}
}

func TestNewStore_RejectsLookalikeSQLiteWithoutFTSIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lookalike.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE documents (
			id INTEGER PRIMARY KEY, path TEXT, hash TEXT, modified_at DATETIME, indexed_at DATETIME
		);
		CREATE TABLE chunks (
			id INTEGER PRIMARY KEY, document_id INTEGER, content TEXT, chunk_index INTEGER, embedding BLOB
		);
		CREATE TABLE embedding_metadata (key TEXT PRIMARY KEY, value TEXT);
		INSERT INTO documents VALUES (1, 'foreign', 'keep', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatalf("seed lookalike database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close lookalike database: %v", err)
	}

	store, err := NewStore(dbPath)
	if store != nil {
		_ = store.Close()
		t.Fatal("NewStore unexpectedly claimed a lookalike SQLite database")
	}
	if !errors.Is(err, ErrNotQuantDatabase) {
		t.Fatalf("NewStore error = %v, want ErrNotQuantDatabase", err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen lookalike database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	if err := db.QueryRow(`SELECT hash FROM documents WHERE id = 1`).Scan(&hash); err != nil || hash != "keep" {
		t.Fatalf("lookalike database changed: hash=%q err=%v", hash, err)
	}
	var applicationID int64
	if err := db.QueryRow(`PRAGMA application_id`).Scan(&applicationID); err != nil {
		t.Fatalf("read lookalike application id: %v", err)
	}
	if applicationID != 0 {
		t.Fatalf("lookalike application_id = %d, want 0", applicationID)
	}
}

func TestNewStore_SetsApplicationID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	var applicationID int64
	if err := store.db.QueryRow(`PRAGMA application_id`).Scan(&applicationID); err != nil {
		t.Fatalf("read application id: %v", err)
	}
	if applicationID != quantApplicationID {
		t.Fatalf("application_id = %d, want %d", applicationID, quantApplicationID)
	}
}

func TestNewStore_IdentityInspectionHandlesSpecialPathCharacters(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant #1?.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	store, err = NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopening special-character path: %v", err)
	}
	defer func() { _ = store.Close() }()
}

func TestMigrateHNSWInvalidationTriggers_InvalidatesLegacyStateOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `
		DROP TRIGGER hnsw_chunks_ai;
		DROP TRIGGER hnsw_chunks_ad;
		DROP TRIGGER hnsw_chunks_au;
		INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions)
		VALUES (1, CURRENT_TIMESTAMP, 1, 'legacy', 2);
	`); err != nil {
		t.Fatalf("prepare legacy trigger state: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	store, err = NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen legacy store: %v", err)
	}
	var stateCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hnsw_state`).Scan(&stateCount); err != nil {
		t.Fatalf("count migrated hnsw_state: %v", err)
	}
	if stateCount != 0 {
		t.Fatalf("legacy hnsw_state count = %d, want 0", stateCount)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions)
		VALUES (1, CURRENT_TIMESTAMP, 1, 'current', 2)
	`); err != nil {
		t.Fatalf("insert current hnsw_state: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}

	store, err = NewStore(dbPath)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hnsw_state`).Scan(&stateCount); err != nil {
		t.Fatalf("count current hnsw_state: %v", err)
	}
	if stateCount != 1 {
		t.Fatalf("current hnsw_state count = %d, want 1", stateCount)
	}
}

func TestBackupStoreFiles_ReturnsRenameError(t *testing.T) {
	dir := t.TempDir()
	err := backupStoreFiles(filepath.Join(dir, "missing.db"), filepath.Join(dir, "backup.db"))
	if err == nil || !strings.Contains(err.Error(), "renaming") {
		t.Fatalf("backupStoreFiles() error = %v, want rename error", err)
	}
}

func TestStore_CloseAfterOperations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "close-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	doc := &Document{Path: "close/doc.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "close test content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestStore_CloseNilStore(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Fatalf("Close() on nil Store error: %v", err)
	}
}

func TestResetIndex(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("reset/doc%d.txt", i)
		id, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       fmt.Sprintf("h%d", i),
			ModifiedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("UpsertDocument(%s) error: %v", path, err)
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: id,
			Content:    "chunk " + path,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("InsertChunk(%s) error: %v", path, err)
		}
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() before reset error: %v", err)
	}
	if docCount != 3 || chunkCount != 3 {
		t.Fatalf("expected 3 docs, 3 chunks before reset, got %d docs, %d chunks", docCount, chunkCount)
	}

	if err := store.resetIndex(ctx); err != nil {
		t.Fatalf("resetIndex() error: %v", err)
	}

	docCount, chunkCount, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() after reset error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected 0 docs, 0 chunks after reset, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestAnyHasVectorCandidate(t *testing.T) {
	empty := anyHasVectorCandidate(nil)
	if empty {
		t.Fatal("expected false for nil candidates")
	}

	noScore := map[int]*searchCandidate{
		1: {id: 1, vectorScore: 0},
		2: {id: 2, vectorScore: 0},
	}
	if anyHasVectorCandidate(noScore) {
		t.Fatal("expected false when no vector scores were computed")
	}

	withScore := map[int]*searchCandidate{
		1: {id: 1, vectorScore: 0},
		2: {id: 2, vectorScore: 0, hasVector: true},
	}
	if !anyHasVectorCandidate(withScore) {
		t.Fatal("expected true when a zero-cosine vector score was computed")
	}
}

func TestPathBoost(t *testing.T) {
	stage := pathBoost([]string{"main", "go"})

	candidates := []scoredCandidate{
		{result: SearchResult{DocumentPath: "src/main.go"}, score: 0.5},
		{result: SearchResult{DocumentPath: "docs/readme.md"}, score: 0.3},
	}
	result := stage(candidates)
	if result[0].score <= 0.5 {
		t.Fatalf("expected score boost for matching path, got %f", result[0].score)
	}
	if result[1].score != 0.3 {
		t.Fatalf("expected unchanged score for non-matching path, got %f", result[1].score)
	}

	noTokens := pathBoost(nil)
	unchanged := []scoredCandidate{{result: SearchResult{DocumentPath: "a.go"}, score: 1.0}}
	result2 := noTokens(unchanged)
	if result2[0].score != 1.0 {
		t.Fatalf("expected unchanged score with no tokens, got %f", result2[0].score)
	}
}

func TestDocEmbeddingWeight_VariousPositions(t *testing.T) {
	w0 := docEmbeddingWeight(0, 1)
	if w0 != 1.0 {
		t.Fatalf("expected 1.0 for single chunk, got %f", w0)
	}

	wFirst := docEmbeddingWeight(0, 3)
	if wFirst <= 1.0 {
		t.Fatalf("expected first chunk bonus > 1.0 for 3 chunks, got %f", wFirst)
	}

	wLast := docEmbeddingWeight(2, 3)
	if wLast <= 1.0 {
		t.Fatalf("expected last chunk bonus > 1.0 for 3 chunks, got %f", wLast)
	}

	wMid := docEmbeddingWeight(1, 3)
	if wMid != 1.0 {
		t.Fatalf("expected 1.0 for middle of 3 chunks, got %f", wMid)
	}

	wFirstBig := docEmbeddingWeight(0, 10)
	if wFirstBig <= 1.0 {
		t.Fatalf("expected first chunk bonus for 10 chunks, got %f", wFirstBig)
	}

	wMiddleBig := docEmbeddingWeight(4, 10)
	if wMiddleBig <= 1.0 {
		t.Fatalf("expected middle weight > 1.0 for 10 chunks, got %f", wMiddleBig)
	}

	wEndBig := docEmbeddingWeight(9, 10)
	if wEndBig <= 1.0 {
		t.Fatalf("expected end bonus for 10 chunks, got %f", wEndBig)
	}
}

func TestDocEmbeddingIndex_SetRemoveTopPaths(t *testing.T) {
	idx := newDocEmbeddingIndex()
	if idx.Len() != 0 {
		t.Fatalf("expected len 0, got %d", idx.Len())
	}

	idx.Set(1, "a.go", []float32{1, 0})
	idx.Set(2, "b.go", []float32{0, 1})
	if idx.Len() != 2 {
		t.Fatalf("expected len 2, got %d", idx.Len())
	}

	topPaths := idx.topDocPaths([]float32{1, 0}, 1)
	if len(topPaths) != 1 {
		t.Fatalf("expected 1 top path, got %d", len(topPaths))
	}
	if _, ok := topPaths["a.go"]; !ok {
		t.Fatal("expected a.go in top paths")
	}

	empty := idx.topDocPaths(nil, 5)
	if empty != nil {
		t.Fatalf("expected nil for nil query, got %v", empty)
	}

	idx.Remove(1, "a.go")
	if idx.Len() != 1 {
		t.Fatalf("expected len 1 after remove, got %d", idx.Len())
	}
}

func TestDocEmbeddingIndex_TopDocPaths_MoreThanTopK(t *testing.T) {
	idx := newDocEmbeddingIndex()
	for i := 0; i < 5; i++ {
		vec := make([]float32, 3)
		vec[i%3] = float32(i + 1)
		idx.Set(int64(i+1), fmt.Sprintf("doc%d.go", i), vec)
	}

	topPaths := idx.topDocPaths([]float32{1, 0, 0}, 2)
	if len(topPaths) != 2 {
		t.Fatalf("expected 2 top paths, got %d", len(topPaths))
	}
}

func TestComputeDocEmbedding_Empty(t *testing.T) {
	result := computeDocEmbedding(nil, 2)
	if result != nil {
		t.Fatalf("expected nil for empty chunks, got %v", result)
	}

	result = computeDocEmbedding([]ChunkRecord{{Embedding: nil, ChunkIndex: 0}}, 2)
	if result != nil {
		t.Fatalf("expected nil for chunks with no valid embeddings, got %v", result)
	}
}

func TestMigrateHNSWStateColumns_AlreadyHasColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if err := store.migrateHNSWStateColumns(); err != nil {
		t.Fatalf("migrateHNSWStateColumns() idempotent error: %v", err)
	}
}

func TestEnsureEmbeddingMetadata_SameMeta(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	meta := EmbeddingMetadata{Model: "same-test", Dimensions: 3, Normalized: true}

	needsReset, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}
	if needsReset {
		t.Fatal("expected no reset for first call on empty DB")
	}

	needsReset2, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if needsReset2 {
		t.Fatal("expected no reset for same meta")
	}
}

func TestEnsureEmbeddingMetadata_DifferentMetaTriggersReset(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	id, err := store.UpsertDocument(ctx, &Document{
		Path: "meta/before.txt", Hash: "h1", ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "before meta change", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}
	dedupHash := ChunkDiffKey("before meta change")
	if err := store.StoreContentDedup(ctx, dedupHash, EncodeFloat32([]float32{1})); err != nil {
		t.Fatalf("StoreContentDedup() error: %v", err)
	}

	meta1 := EmbeddingMetadata{Model: "model-a", Dimensions: 2, Normalized: true}
	_, err = store.EnsureEmbeddingMetadata(ctx, meta1)
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}

	meta2 := EmbeddingMetadata{Model: "model-b", Dimensions: 4, Normalized: false}
	needsReset, err := store.EnsureEmbeddingMetadata(ctx, meta2)
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if !needsReset {
		t.Fatal("expected reset for different meta")
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected 0 docs, 0 chunks after reset, got %d, %d", docCount, chunkCount)
	}
	if _, found := store.LookupContentDedup(ctx, dedupHash); found {
		t.Fatal("expected embedding metadata reset to invalidate content dedup")
	}
}

func TestCleanupOrphanedChunks_NoOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{
		Path: "clean/doc.txt", Hash: "h1", ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "clean chunk", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	if err := store.cleanupOrphanedChunks(ctx); err != nil {
		t.Fatalf("cleanupOrphanedChunks() no orphans error: %v", err)
	}

	_, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if chunkCount != 1 {
		t.Fatalf("expected 1 chunk unchanged, got %d", chunkCount)
	}
}
