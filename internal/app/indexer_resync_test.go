package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/watch"
)

func TestConfigureProcessMemory_WithEnvVar(t *testing.T) {
	orig := os.Getenv("GOMEMLIMIT")
	t.Setenv("GOMEMLIMIT", "1GiB")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("GOMEMLIMIT")
		} else {
			_ = os.Setenv("GOMEMLIMIT", orig)
		}
	}()
	configureProcessMemory()
}

func TestConfigureProcessMemory_Default(t *testing.T) {
	orig := os.Getenv("GOMEMLIMIT")
	_ = os.Unsetenv("GOMEMLIMIT")
	defer func() {
		if orig != "" {
			_ = os.Setenv("GOMEMLIMIT", orig)
		}
	}()
	configureProcessMemory()
}

func TestSameModTime_MicrosecondPrecision(t *testing.T) {
	a := time.Date(2025, 1, 1, 0, 0, 0, 1000, time.UTC)
	b := time.Date(2025, 1, 1, 0, 0, 0, 2000, time.UTC)
	if SameModTime(a, b) {
		t.Fatal("expected different microsecond times to not be equal")
	}
}

func TestSameModTime_DifferentZone(t *testing.T) {
	loc := time.FixedZone("offset", 3600)
	a := time.Date(2025, 6, 15, 12, 0, 0, 0, loc)
	b := time.Date(2025, 6, 15, 11, 0, 0, 0, time.UTC)
	if !SameModTime(a, b) {
		t.Fatal("expected same instant in different zones to be equal")
	}
}

func TestResyncCoordinator_BeginAlreadyRunning(t *testing.T) {
	rc := NewResyncCoordinator(ResyncCallbacks{})
	if !rc.begin() {
		t.Fatal("expected first begin to succeed")
	}
	if rc.begin() {
		t.Fatal("expected second begin while running to return false")
	}
}

func TestResyncCoordinator_FinishNoPending(t *testing.T) {
	rc := NewResyncCoordinator(ResyncCallbacks{})
	rc.begin()
	if rc.finish(false) {
		t.Fatal("expected finish with no pending to return false")
	}
}

func TestResyncCoordinator_FinishWithPending(t *testing.T) {
	rc := NewResyncCoordinator(ResyncCallbacks{})
	rc.begin()
	rc.pending = true
	if !rc.finish(true) {
		t.Fatal("expected finish with pending to return true")
	}
}

func TestResyncCoordinator_ErrorWithoutContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errorCalled := false
	rc := NewResyncCoordinator(ResyncCallbacks{
		OnStartup: func(context.Context) (SyncReport, error) {
			errorCalled = true
			return SyncReport{}, errors.New("sync error")
		},
		OnState: func(state runtimestate.IndexState, message string) {},
	})

	rc.RunInitialSync(ctx)
	if !errorCalled {
		t.Fatal("expected error callback to be called")
	}
}

func TestResyncCoordinator_ResyncSuccessState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stateCalled := false
	rc := NewResyncCoordinator(ResyncCallbacks{
		OnResync: func(context.Context) (SyncReport, error) {
			return SyncReport{}, nil
		},
		OnState: func(state runtimestate.IndexState, message string) {
			if state == runtimestate.IndexStateReady && message == "filesystem resync complete" {
				stateCalled = true
			}
		},
	})

	rc.running = true
	rc.runLoop(ctx, false)
	if !stateCalled {
		t.Fatal("expected ready state to be set after successful resync")
	}
}

func TestInitialSyncWithReport_LoadGitIgnoreError(t *testing.T) {
	root := t.TempDir()
	nonexistent := filepath.Join(root, "nonexistent")
	cfg := config.Default()
	cfg.WatchDir = nonexistent
	cfg.DBPath = filepath.Join(root, "quant.db")

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	_, err := idx.InitialSyncWithReport(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent watch dir")
	}
}

func TestInitialSyncWithReport_ListDocumentsError(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &errorListStore{},
		Quarantine: &stubQuarantineStore{},
	})

	_, err := idx.InitialSyncWithReport(context.Background())
	if err == nil {
		t.Fatal("expected error from ListDocuments failure")
	}
}

type errorListStore struct {
	stubDocumentStore
}

func (e *errorListStore) ListDocuments(context.Context) ([]index.Document, error) {
	return nil, errors.New("db error")
}

func TestRunHNSWReoptimizer_NotReady(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	store := &trackingReoptStore{ready: false, needReopt: true}

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
		t.Fatal("expected no HNSW build when not ready")
	}
}

type trackingReoptStore struct {
	ready     bool
	needReopt bool
	buildErr  error
	built     bool
}

func (s *trackingReoptStore) HNSWReady() bool                         { return s.ready }
func (s *trackingReoptStore) BuildHNSW(_ context.Context) error       { s.built = true; return s.buildErr }
func (s *trackingReoptStore) HNSWReoptimizationNeeded(_ float64) bool { return s.needReopt }

func TestRunHNSWReoptimizer_BuildError(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	store := &trackingReoptStore{ready: true, needReopt: true, buildErr: errors.New("build failed")}

	done := make(chan struct{})
	go func() {
		idx.RunHNSWReoptimizer(ctx, store, 0.5)
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunHNSWReoptimizer did not exit on context cancellation")
	}
}

func TestRunPeriodicVacuum_VacuumError(t *testing.T) {
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

func TestWatchLoop_ContextCancelled(t *testing.T) {
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

func TestSyncDocumentOnce_StatErrorPermission(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("permission_test", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "permission_test", "/nonexistent/deeply/nested/path/that/should/not/exist.txt", nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for stat error on deep nonexistent path, got %s", action)
	}
}

func TestHandleWatchEvent_RemoveWithDocumentInStore(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "a.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  filepath.Join(root, "a.txt"),
		Op:    watch.Remove,
		IsDir: false,
	})

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "a.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected document to be deleted on Remove event with doc in store")
	}
}

func TestHandleWatchEvent_CreateNonExistentPath(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	idx := newTestIndexer(root, store, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	nonexistent := filepath.Join(root, "ghost", "file.txt")
	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  nonexistent,
		Op:    watch.Create,
		IsDir: false,
	})
}

func TestInitialSync_WithValidDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 4

	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	hnsw := &stubHNSWBuilder{ready: true}
	idx := NewIndexer(IndexerConfig{
		Cfg:       cfg,
		Store:     store,
		HNSWStore: hnsw,
		Extractor: &stubExtractor{},
		Embedder:  &stubEmbedder{dimensions: 3},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := idx.InitialSync(ctx)
	_ = err
}

func TestSyncDocument_SameFileSameModTime(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "same.txt")
	writeFile(t, file, "hello")

	ext := &stubExtractor{supportsPath: file, text: "hello"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	modTime := time.Now()
	action, err := idx.SyncDocument(context.Background(), "same.txt", file, &modTime, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated, got %s", action)
	}

	action2, err := idx.SyncDocument(context.Background(), "same.txt", file, &modTime, nil)
	if err != nil {
		t.Fatalf("unexpected error on second sync: %v", err)
	}
	_ = action2
}

func TestGetPipeline_NilPipelineWithDefaultBatchSize(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.ChunkSize = 64
	cfg.ChunkOverlap = 0
	cfg.EmbedBatchSize = 0

	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        cfg,
		embedder:   emb,
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	p := idx.getPipeline()
	if p == nil {
		t.Fatal("expected pipeline to be created")
	}
	if p.BatchSize != 16 {
		t.Fatalf("expected default batch size 16, got %d", p.BatchSize)
	}
}

func TestHandleWatchEvent_IgnoresDBCompanion(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.1
	cfg.EmbedBatchSize = 4

	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		extractor:  &stubExtractor{},
		embedder:   &stubEmbedder{dimensions: 3},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(16),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	logPath := LogPathForDB(cfg.DBPath)
	writeFile(t, logPath, "log data")

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  logPath,
		Op:    watch.Create,
		IsDir: false,
	})
}

func TestEmbedErrPermanentRetryBehavior(t *testing.T) {
	if shouldRetryIndexError(embed.ErrPermanent) {
		t.Fatal("expected permanent embed errors to not be retried")
	}
	if !shouldQuarantineIndexError(embed.ErrPermanent) {
		t.Fatal("expected permanent embed errors to be quarantined")
	}
}

func TestFormatMemoryLimit_KB(t *testing.T) {
	got := formatMemoryLimit(1024)
	if got != "1.0 KB" {
		t.Fatalf("expected 1.0 KB, got %s", got)
	}
}

func TestFormatMemoryLimit_TB(t *testing.T) {
	got := formatMemoryLimit(1 << 40)
	if got != "1.0 TB" {
		t.Fatalf("expected 1.0 TB, got %s", got)
	}
}

func TestFormatMemoryLimit_Bytes(t *testing.T) {
	got := formatMemoryLimit(500)
	if got != "500 B" {
		t.Fatalf("expected 500 B, got %s", got)
	}
}

func TestWorkerCount(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.IndexWorkers = 8

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	if got := idx.workerCount(4); got != 4 {
		t.Fatalf("expected worker count capped to 4, got %d", got)
	}
	if got := idx.workerCount(0); got != 8 {
		t.Fatalf("expected full worker count 8, got %d", got)
	}
}

func TestWorkerCount_ZeroWorkers(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.IndexWorkers = 0

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	if got := idx.workerCount(0); got != 1 {
		t.Fatalf("expected minimum worker count 1, got %d", got)
	}
}

func TestNewIndexer_NilDefaults(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	if idx == nil {
		t.Fatal("expected indexer to be created")
	}
	if idx.pipeline == nil {
		t.Fatal("expected pipeline to be initialized")
	}
}

func TestHNSWFlush_FlushError(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	ctx, cancel := context.WithCancel(context.Background())
	flushStore := &stubHNSWFlushStore{ready: true, flushErr: errors.New("flush failed")}

	done := make(chan struct{})
	go func() {
		idx.RunHNSWPeriodicFlush(ctx, flushStore)
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunHNSWPeriodicFlush did not exit on context cancellation")
	}
}
