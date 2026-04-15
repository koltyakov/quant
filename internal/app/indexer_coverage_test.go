package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

type trackingDocumentStore struct {
	mu            sync.Mutex
	reindexed     []*index.Document
	deletedPaths  []string
	deletedPrefix string
	docByPath     *index.Document
	chunksByPath  map[string]index.ChunkRecord
	deleteErr     error
	reindexErr    error
	getDocErr     error
}

func (s *trackingDocumentStore) ReindexDocument(_ context.Context, doc *index.Document, _ []index.ChunkRecord) error {
	s.mu.Lock()
	s.reindexed = append(s.reindexed, doc)
	s.mu.Unlock()
	return s.reindexErr
}

func (s *trackingDocumentStore) DeleteDocument(_ context.Context, path string) error {
	s.mu.Lock()
	s.deletedPaths = append(s.deletedPaths, path)
	s.mu.Unlock()
	return s.deleteErr
}

func (s *trackingDocumentStore) DeleteDocumentsByPrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	s.deletedPrefix = prefix
	s.mu.Unlock()
	return nil
}

func (s *trackingDocumentStore) RenameDocumentPath(context.Context, string, string) error { return nil }
func (s *trackingDocumentStore) GetDocumentByPath(_ context.Context, _ string) (*index.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docByPath, s.getDocErr
}
func (s *trackingDocumentStore) ListDocuments(context.Context) ([]index.Document, error) {
	return nil, nil
}
func (s *trackingDocumentStore) ListDocumentsLimit(context.Context, int) ([]index.Document, error) {
	return nil, nil
}
func (s *trackingDocumentStore) GetChunkByID(context.Context, int64) (*index.SearchResult, error) {
	return nil, nil
}
func (s *trackingDocumentStore) GetDocumentChunksByPath(_ context.Context, _ string) (map[string]index.ChunkRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chunksByPath, nil
}
func (s *trackingDocumentStore) Stats(context.Context) (int, int, error) { return 0, 0, nil }

type stubExtractor struct {
	supportsPath string
	text         string
	err          error
}

func (e *stubExtractor) Extract(_ context.Context, _ string) (string, error) {
	if e.err != nil {
		return "", e.err
	}
	return e.text, nil
}

func (e *stubExtractor) Supports(path string) bool {
	return e.supportsPath != "" && path == e.supportsPath
}

type stubEmbedder struct {
	dimensions int
	err        error
}

func (e *stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	vec := make([]float32, e.dimensions)
	return vec, nil
}

func (e *stubEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	result := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, e.dimensions)
		result[i] = vec
	}
	return result, nil
}

func (e *stubEmbedder) Dimensions() int { return e.dimensions }
func (e *stubEmbedder) Close() error    { return nil }

type stubHNSWBuilder struct {
	ready    bool
	built    bool
	loaded   bool
	buildErr error
}

func (s *stubHNSWBuilder) HNSWReady() bool                          { return s.ready }
func (s *stubHNSWBuilder) BuildHNSW(_ context.Context) error        { s.built = true; return s.buildErr }
func (s *stubHNSWBuilder) RemoveBackup()                            {}
func (s *stubHNSWBuilder) LoadHNSWFromState(_ context.Context) bool { s.loaded = true; return s.ready }
func (s *stubHNSWBuilder) FlushHNSW() error                         { return nil }

type stubHNSWReoptimizeStore struct {
	ready     bool
	needReopt bool
	buildErr  error
}

func (s *stubHNSWReoptimizeStore) HNSWReady() bool                         { return s.ready }
func (s *stubHNSWReoptimizeStore) BuildHNSW(_ context.Context) error       { return s.buildErr }
func (s *stubHNSWReoptimizeStore) HNSWReoptimizationNeeded(_ float64) bool { return s.needReopt }

type stubVacuumStore struct {
	vacuumErr error
}

func (s *stubVacuumStore) Vacuum(_ context.Context) error { return s.vacuumErr }

type stubHNSWFlushStore struct {
	ready    bool
	flushErr error
}

func (s *stubHNSWFlushStore) HNSWReady() bool                   { return s.ready }
func (s *stubHNSWFlushStore) FlushHNSW() error                  { return s.flushErr }
func (s *stubHNSWFlushStore) BuildHNSW(_ context.Context) error { return nil }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestIndexer(root string, store index.DocumentWriter, ext extract.Extractor, emb embed.Embedder) *Indexer {
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 4
	return NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      store,
		Extractor:  ext,
		Embedder:   emb,
		Quarantine: &stubQuarantineStore{},
	})
}

func newSyncTestIndexer(root string, store *trackingDocumentStore, ext extract.Extractor, emb embed.Embedder) *Indexer {
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 4
	return NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      store,
		Extractor:  ext,
		Embedder:   emb,
		Quarantine: &stubQuarantineStore{},
	})
}

func TestProcessLiveIndexRequestDirect_Indexes(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "hello world")

	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	ext := &stubExtractor{supportsPath: file, text: "hello world"}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	idx.processLiveIndexRequestDirect(context.Background(), file, time.Now())

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed")
	}
}

func TestProcessLiveIndexRequestDirect_FileNotFound(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)
	idx.live = nil

	path := filepath.Join(root, "nonexistent.txt")
	idx.processLiveIndexRequestDirect(context.Background(), path, time.Now())
}

func TestSyncDocument_FileNotFound(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "docs/gone.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	action, err := idx.SyncDocument(context.Background(), "docs/gone.txt", filepath.Join(root, "docs", "gone.txt"), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected removed for missing file with doc in store, got %s", action)
	}
}

func TestSyncDocument_DirectoryPath(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	action, err := idx.SyncDocument(context.Background(), "subdir", dir, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for directory, got %s", action)
	}
}

func TestSyncDocument_FileFound(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	action, err := idx.SyncDocument(context.Background(), "docs/a.txt", file, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated, got %s", action)
	}
}

func TestSyncDocumentOnce_FileNotFound_RemoveFromIndex(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "docs/old.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("docs/old.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "docs/old.txt", filepath.Join(root, "docs", "old.txt"), nil, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected removed for nonexistent file, got %s", action)
	}

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "docs/old.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected delete call for docs/old.txt")
	}
}

func TestSyncDocumentOnce_FileNotFound_NoDoc(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: nil}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("docs/ghost.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "docs/ghost.txt", filepath.Join(root, "docs", "ghost.txt"), nil, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop when file not found and no doc, got %s", action)
	}
}

func TestSyncDocumentOnce_IsDirectory_Noop(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	version, started := idx.paths.Begin("subdir", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "subdir", dir, nil, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for directory, got %s", action)
	}
}

func TestSyncDocumentOnce_UnsupportedExtractor_RemoveFromIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.dat")
	writeFile(t, file, "binary data")

	store := &trackingDocumentStore{docByPath: &index.Document{Path: "docs/a.dat", Hash: "old"}}
	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("docs/a.dat", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "docs/a.dat", file, nil, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected removed for unsupported type with existing doc, got %s", action)
	}
}

func TestLoadDocument_PreSupplied(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)

	doc := &index.Document{Path: "docs/a.txt", Hash: "abc"}
	result, err := idx.loadDocument(context.Background(), "docs/a.txt", doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != doc {
		t.Fatal("expected pre-supplied doc to be returned")
	}
}

func TestLoadDocument_StoreLookup(t *testing.T) {
	root := t.TempDir()
	storedDoc := &index.Document{Path: "docs/a.txt", Hash: "def"}
	store := &trackingDocumentStore{docByPath: storedDoc}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)

	result, err := idx.loadDocument(context.Background(), "docs/a.txt", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != storedDoc {
		t.Fatal("expected store doc to be returned")
	}
}

func TestLoadDocument_StoreError(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{getDocErr: errors.New("db error")}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)

	_, err := idx.loadDocument(context.Background(), "docs/a.txt", nil)
	if err == nil {
		t.Fatal("expected error from store lookup")
	}
}

func TestShouldIndexExistingPath_FileNotFound(t *testing.T) {
	root := t.TempDir()
	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, filepath.Join(root, "nonexistent.txt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for nonexistent file")
	}
}

func TestShouldIndexExistingPath_Directory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for directory")
	}
}

func TestShouldIndexExistingPath_IgnoredPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "logs", "a.log")
	writeFile(t, file, "data")

	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ExcludePatterns = []string{"logs/**"}
	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		extractor:  &stubExtractor{supportsPath: file},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for excluded path")
	}
}

func TestShouldIndexExistingPath_UnsupportedType(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.xyz")
	writeFile(t, file, "data")

	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for unsupported file type")
	}
}

func TestShouldIndexExistingPath_SupportedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "data")

	ext := &stubExtractor{supportsPath: file}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true for supported file not ignored")
	}
}

func TestIndexFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.txt")
	writeFile(t, file, "hello")

	ext := &stubExtractor{supportsPath: file, text: "hello"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	action, err := idx.IndexFile(context.Background(), file, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated, got %s", action)
	}
}

func TestGetPipeline_NilPipeline(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.ChunkSize = 128
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 8
	emb := &stubEmbedder{dimensions: 3}

	idx := &Indexer{
		cfg:        cfg,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
		pipeline:   nil,
	}

	p := idx.getPipeline()
	if p == nil {
		t.Fatal("expected pipeline to be created")
	}
	if p.ChunkSize != 128 {
		t.Fatalf("expected chunk size 128, got %d", p.ChunkSize)
	}
	if p.Embedder != emb {
		t.Fatal("expected embedder to be set")
	}

	p2 := idx.getPipeline()
	if p2 != p {
		t.Fatal("expected same pipeline instance on second call")
	}
}

func TestIndexFileCore_HashMatchNoop(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "hello")

	hash, err := scan.FileHash(file)
	if err != nil {
		t.Fatal(err)
	}

	ext := &stubExtractor{supportsPath: file, text: "hello"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	doc := &index.Document{Path: "a.txt", Hash: hash, ModifiedAt: time.Now()}
	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), hash, doc, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop when hash matches, got %s", action)
	}
}

func TestIndexFileCore_NewFileGetsIndexed(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "hello world")

	ext := &stubExtractor{supportsPath: file, text: "hello world"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "", nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated, got %s", action)
	}
}

func TestIndexFileCore_ExtractError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, err: errors.New("extraction failed")}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	_, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "", nil, version)
	if err == nil {
		t.Fatal("expected extraction error")
	}
}

func TestIndexFileCore_EmptyTextRemoves(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: ""}
	emb := &stubEmbedder{dimensions: 3}
	doc := &index.Document{Path: "a.txt", Hash: "old"}
	store := &trackingDocumentStore{docByPath: doc, chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "", doc, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected removed for empty text extraction with existing doc, got %s", action)
	}
}

func TestHandleWatchEvent_Create(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	idx.HandleWatchEvent(context.Background(), watch.Event{Path: file, Op: watch.Create, IsDir: false})

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed on Create event")
	}
}

func TestHandleWatchEvent_Write(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	idx.HandleWatchEvent(context.Background(), watch.Event{Path: file, Op: watch.Write, IsDir: false})

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed on Write event")
	}
}

func TestHandleWatchEvent_Remove(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "a.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	idx.HandleWatchEvent(context.Background(), watch.Event{Path: filepath.Join(root, "a.txt"), Op: watch.Remove, IsDir: false})

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "a.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected delete call for Remove event")
	}
}

func TestHandleWatchEvent_Resync(t *testing.T) {
	root := t.TempDir()
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	resyncDone := make(chan struct{})
	idx.initResyncCoordinator()
	origOnResync := idx.Resync
	_ = origOnResync
	idx.Resync = NewResyncCoordinator(ResyncCallbacks{
		OnResync: func(context.Context) (SyncReport, error) {
			close(resyncDone)
			return SyncReport{}, nil
		},
	})

	idx.HandleWatchEvent(context.Background(), watch.Event{Path: root, Op: watch.Resync, IsDir: true})
	select {
	case <-resyncDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expected resync to be requested")
	}
}

func TestHandleWatchEvent_RemoveDirectory(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	event := watch.Event{Path: filepath.Join(root, "docs"), Op: watch.Remove, IsDir: true}
	idx.HandleWatchEvent(context.Background(), event)

	store.mu.Lock()
	got := store.deletedPrefix
	store.mu.Unlock()
	if got != "docs" {
		t.Fatalf("expected deleted prefix 'docs', got %q", got)
	}
}

func TestRunHNSWReoptimizer_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	store := &stubHNSWReoptimizeStore{ready: true, needReopt: true}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	idx.RunHNSWReoptimizer(ctx, store, 0.5)
}

func TestRunPeriodicVacuum_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	vacStore := &stubVacuumStore{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	idx.RunPeriodicVacuum(ctx, vacStore)
}

func TestRunHNSWPeriodicFlush_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	flushStore := &stubHNSWFlushStore{ready: true}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	idx.RunHNSWPeriodicFlush(ctx, flushStore)
}

func TestWatchLoop_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())

	watcher, err := watch.New(root, nil)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	done := make(chan struct{})
	go func() {
		idx.WatchLoop(ctx, watcher)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchLoop did not exit on context cancellation")
	}
}

func TestInitResyncCoordinator(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})
	idx.Resync = nil

	idx.initResyncCoordinator()
	if idx.Resync == nil {
		t.Fatal("expected Resync to be initialized")
	}
}

func TestRunInitialSync_InitializesResyncWhenNil(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	hnsw := &stubHNSWBuilder{ready: true}
	idx := NewIndexer(IndexerConfig{
		Cfg: &config.Config{
			WatchDir:       root,
			DBPath:         filepath.Join(root, "quant.db"),
			ChunkSize:      256,
			ChunkOverlap:   0.1,
			EmbedBatchSize: 4,
		},
		Store:      store,
		HNSWStore:  hnsw,
		Extractor:  &stubExtractor{},
		Embedder:   &stubEmbedder{dimensions: 3},
		Quarantine: &stubQuarantineStore{},
	})
	idx.Resync = nil

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	idx.RunInitialSync(ctx)
	if idx.Resync == nil {
		t.Fatal("expected Resync to be initialized by RunInitialSync")
	}
}

func TestRequestResync_InitializesResyncWhenNil(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})
	idx.Resync = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idx.RequestResync(ctx)
	if idx.Resync == nil {
		t.Fatal("expected Resync to be initialized")
	}
}

func TestInitialSync_Delegates(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	hnsw := &stubHNSWBuilder{ready: true}
	idx := NewIndexer(IndexerConfig{
		Cfg: &config.Config{
			WatchDir:       root,
			DBPath:         filepath.Join(root, "quant.db"),
			ChunkSize:      256,
			ChunkOverlap:   0.1,
			EmbedBatchSize: 4,
		},
		Store:      store,
		HNSWStore:  hnsw,
		Extractor:  &stubExtractor{},
		Embedder:   &stubEmbedder{dimensions: 3},
		Quarantine: &stubQuarantineStore{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := idx.InitialSync(ctx)
	_ = err
}

func TestConfigureProcessMemory_NoPanic(t *testing.T) {
	configureProcessMemory()
}

func TestReclaimProcessMemory_NoPanic(t *testing.T) {
	reclaimProcessMemory()
}

func TestQuarantineFailedPath_NilIndexer(t *testing.T) {
	var idx *Indexer
	idx.quarantineFailedPath(context.Background(), "", nil)
}

func TestQuarantineFailedPath_EmptyPath(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	quarantine := &stubQuarantineStore{}
	idx := newTestIndexer(root, store, &stubExtractor{}, &stubEmbedder{dimensions: 3})
	idx.quarantine = quarantine

	idx.quarantineFailedPath(context.Background(), "", errors.New("some error"))
	if quarantine.addedPath != "" {
		t.Fatal("expected no quarantine for empty path")
	}
}

func TestQuarantineFailedPath_NilFailure(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})
	idx.quarantine = quarantine

	idx.quarantineFailedPath(context.Background(), filepath.Join(root, "a.txt"), nil)
	if quarantine.addedPath != "" {
		t.Fatal("expected no quarantine for nil failure")
	}
}

func TestQuarantineFailedPath_Success(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	idx := newTestIndexer(root, &trackingDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})
	idx.quarantine = quarantine

	file := filepath.Join(root, "a.txt")
	idx.quarantineFailedPath(context.Background(), file, errors.New("permanent failure"))
	if quarantine.addedPath != "a.txt" {
		t.Fatalf("expected quarantined path 'a.txt', got %q", quarantine.addedPath)
	}
}
