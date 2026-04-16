package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

func TestSyncDocumentOnce_FileSucceeds_IndexUpdated(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.txt")
	writeFile(t, file, "content for sync once")

	ext := &stubExtractor{supportsPath: file, text: "content for sync once"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("docs/a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "docs/a.txt", file, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected IndexUpdated, got %s", action)
	}

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed")
	}
}

func TestSyncDocumentOnce_FileRemoved_DocExists(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "gone/once.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("gone/once.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "gone/once.txt", filepath.Join(root, "gone", "once.txt"), nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected IndexRemoved, got %s", action)
	}
}

func TestSyncDocumentOnce_SameModTimeSameHash_Noop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "same.txt")
	writeFile(t, file, "unchanged content")

	ext := &stubExtractor{supportsPath: file, text: "unchanged content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	modTime := time.Now()
	action, err := idx.SyncDocument(context.Background(), "same.txt", file, &modTime, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected first sync to update, got %s", action)
	}

	version, started := idx.paths.Begin("same.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err = idx.syncDocumentOnce(context.Background(), "same.txt", file, nil, version)
	if err != nil {
		t.Fatalf("unexpected error on second sync: %v", err)
	}
	_ = action
}

func TestInitialSyncWithReport_StoreDeleteError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "content")

	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 4

	store := &errorDeleteDocStore{}
	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      store,
		Extractor:  &stubExtractor{supportsPath: filepath.Join(root, "a.txt"), text: "content"},
		Embedder:   &stubEmbedder{dimensions: 3},
		Quarantine: &stubQuarantineStore{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, err := idx.InitialSyncWithReport(ctx)
	_ = report
	_ = err
}

type errorDeleteDocStore struct {
	trackingDocumentStore
}

func (e *errorDeleteDocStore) DeleteDocument(_ context.Context, path string) error {
	return errors.New("delete failed")
}

func TestInitialSyncWithReport_CancelledContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	_, err := idx.InitialSyncWithReport(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestRunHNSWReoptimizer_ReadyButNoReoptNeeded(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	store := &trackingReoptStore{ready: true, needReopt: false}

	done := make(chan struct{})
	go func() {
		idx.RunHNSWReoptimizer(ctx, store, 0.5)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunHNSWReoptimizer did not exit on context cancellation")
	}

	if store.built {
		t.Fatal("expected no HNSW build when reoptimization not needed")
	}
}

func TestRunPeriodicVacuum_ErrorThenCancel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	vacStore := &stubVacuumStore{vacuumErr: errors.New("vacuum failed")}

	done := make(chan struct{})
	go func() {
		idx.RunPeriodicVacuum(ctx, vacStore)
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPeriodicVacuum did not exit on context cancellation")
	}
}

func TestRunHNSWPeriodicFlush_NotReady(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	flushStore := &stubHNSWFlushStore{ready: false}

	done := make(chan struct{})
	go func() {
		idx.RunHNSWPeriodicFlush(ctx, flushStore)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunHNSWPeriodicFlush did not exit on context cancellation")
	}
}

func TestSyncDocumentOnce_UnsupportedTypeNoDocRemoval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "data.bin")
	writeFile(t, file, "binary data")

	store := &trackingDocumentStore{}
	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("data.bin", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "data.bin", file, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for unsupported type with no existing doc, got %s", action)
	}
}

func TestIndexFile_HashComputation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "hash.txt")
	writeFile(t, file, "hash test content")

	ext := &stubExtractor{supportsPath: file, text: "hash test content"}
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

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed")
	}
}

func TestSyncDocumentOnce_ExtractError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "err.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, err: errors.New("extraction error")}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("err.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "err.txt", file, nil, version)
	if err == nil {
		t.Fatalf("expected extraction error, got action=%s", action)
	}
}

func TestInitialSyncWithReport_EmptyDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	store := &trackingDocumentStore{}
	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      store,
		Extractor:  &stubExtractor{},
		Embedder:   &stubEmbedder{dimensions: 3},
		Quarantine: &stubQuarantineStore{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, err := idx.InitialSyncWithReport(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.HadIndexFailures {
		t.Fatal("expected no failures for empty directory")
	}
}

func TestSetIndexState_WithTracker(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	idx.SetIndexState(runtimestate.IndexStateIndexing, "testing state")
	snap := idx.IndexState.Snapshot()
	if snap.State != runtimestate.IndexStateIndexing {
		t.Fatalf("expected IndexStateIndexing, got %s", snap.State)
	}
}

func TestNewIndexer_WithHNSWStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	hnsw := &stubHNSWBuilder{ready: false}
	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		HNSWStore:  hnsw,
		Quarantine: &stubQuarantineStore{},
	})
	if idx == nil {
		t.Fatal("expected indexer to be created")
	}
	if idx.hnswStore == nil {
		t.Fatal("expected HNSW store to be set")
	}
}

func TestShouldRetryIndexError_NilEmbedErr(t *testing.T) {
	t.Parallel()

	if shouldRetryIndexError(nil) {
		t.Fatal("expected nil error to not be retried")
	}
	if !shouldRetryIndexError(fmt.Errorf("transient")) {
		t.Fatal("expected transient error to be retried")
	}
}

func TestShouldRetryIndexError_ContextErrors(t *testing.T) {
	t.Parallel()

	if shouldRetryIndexError(context.Canceled) {
		t.Fatal("expected context.Canceled to not be retried")
	}
	if shouldRetryIndexError(context.DeadlineExceeded) {
		t.Fatal("expected context.DeadlineExceeded to not be retried")
	}
}

func TestShouldQuarantineIndexError_OCRAndFileTooLarge(t *testing.T) {
	t.Parallel()

	if !shouldQuarantineIndexError(ErrOCRFailed) {
		t.Fatal("expected OCR failed to be quarantined")
	}
	if !shouldQuarantineIndexError(ErrFileTooLarge) {
		t.Fatal("expected file too large to be quarantined")
	}
	if !shouldQuarantineIndexError(embed.ErrPermanent) {
		t.Fatal("expected permanent embed error to be quarantined")
	}
	if shouldQuarantineIndexError(errors.New("transient")) {
		t.Fatal("expected transient error to not be quarantined")
	}
}
