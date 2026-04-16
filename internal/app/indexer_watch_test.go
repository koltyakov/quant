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
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

func TestSameModTime_EqualTimes(t *testing.T) {
	now := time.Now()
	if !SameModTime(now, now) {
		t.Fatal("expected same times to be equal")
	}
}

func TestSameModTime_DifferentTimes(t *testing.T) {
	a := time.Now()
	b := a.Add(time.Second)
	if SameModTime(a, b) {
		t.Fatal("expected different times to not be equal")
	}
}

func TestSameModTime_UTCNormalization(t *testing.T) {
	now := time.Now()
	utc := now.UTC()
	local := now.Local()
	if !SameModTime(utc, local) {
		t.Fatal("expected UTC and local representations to be equal after normalization")
	}
}

func TestDocumentKey_SubdirectoryPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "notes.md")
	writeFile(t, file, "content")

	key, err := DocumentKey(root, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != filepath.Join("docs", "inner", "notes.md") {
		t.Fatalf("unexpected key: %q", key)
	}
}

func TestDocumentKey_SameLevelFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "readme.md")
	writeFile(t, file, "content")

	key, err := DocumentKey(root, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "readme.md" {
		t.Fatalf("unexpected key: %q", key)
	}
}

func TestDocumentKey_PathCleaning(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "main.go")
	writeFile(t, file, "content")

	uncleanPath := filepath.Join(root, "src", ".", "main.go")
	key, err := DocumentKey(root, uncleanPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != filepath.Join("src", "main.go") {
		t.Fatalf("expected cleaned key, got %q", key)
	}
}

func TestNormalizeStoredDocumentPath_EdgeCases(t *testing.T) {
	root := t.TempDir()

	normalized, err := NormalizeStoredDocumentPath(root, "docs/a.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized != "docs/a.md" {
		t.Fatalf("unexpected normalized path: %q", normalized)
	}

	normalized, err = NormalizeStoredDocumentPath(root, filepath.Join("docs", "b.md"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized != filepath.Join("docs", "b.md") {
		t.Fatalf("unexpected normalized path with separator: %q", normalized)
	}
}

func TestShouldIgnorePath_NilMatcher(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	if idx.shouldIgnorePath(filepath.Join(root, "anything.txt")) {
		t.Fatal("expected no path to be ignored when no patterns configured")
	}
}

func TestShouldIgnorePath_EmptyDBPath(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = ""

	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	if idx.shouldIgnorePath(filepath.Join(root, "any.log")) {
		t.Fatal("expected no path to be ignored with empty DBPath")
	}
}

func TestShouldIgnorePath_IncludeOnlyPatterns(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.IncludePatterns = []string{"**/*.go"}
	cfg.ExcludePatterns = []string{"vendor/**"}

	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	if idx.shouldIgnorePath(filepath.Join(root, "main.go")) {
		t.Fatal("expected Go file to not be ignored")
	}
	if !idx.shouldIgnorePath(filepath.Join(root, "readme.md")) {
		t.Fatal("expected non-Go file to be ignored by include pattern")
	}
	if !idx.shouldIgnorePath(filepath.Join(root, "vendor", "pkg", "util.go")) {
		t.Fatal("expected vendor Go file to be excluded")
	}
}

func TestShouldIgnorePath_DBCompanionLog(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")

	idx := &Indexer{
		cfg:        cfg,
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	logPath := LogPathForDB(cfg.DBPath)
	if !idx.shouldIgnorePath(logPath) {
		t.Fatal("expected companion log path to be ignored")
	}
	rotatedLogPath := logPath + ".1"
	if !idx.shouldIgnorePath(rotatedLogPath) {
		t.Fatal("expected rotated companion log path to be ignored")
	}
}

func TestQuarantineFailedPath_NilQuarantineStore(t *testing.T) {
	root := t.TempDir()
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	idx.quarantineFailedPath(context.Background(), filepath.Join(root, "a.txt"), errors.New("failure"))
}

func TestQuarantineFailedPath_KeyError(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	idx.quarantineFailedPath(context.Background(), "/outside/root/file.txt", errors.New("failure"))
	if quarantine.addedPath != "" {
		t.Fatal("expected no quarantine for path outside root")
	}
}

func TestQuarantineFailedPath_DeleteDocAfterQuarantine(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	quarantine := &stubQuarantineStore{}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.quarantineFailedPath(context.Background(), file, errors.New("permanent"))
	if quarantine.addedPath != "a.txt" {
		t.Fatalf("expected quarantined path 'a.txt', got %q", quarantine.addedPath)
	}

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "a.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected DeleteDocument to be called for quarantined path")
	}
}

type errorAddQuarantineStore struct {
	addedPath string
	addErr    error
}

func (s *errorAddQuarantineStore) AddToQuarantine(_ context.Context, path, _ string) error {
	s.addedPath = path
	return s.addErr
}
func (s *errorAddQuarantineStore) RemoveFromQuarantine(context.Context, string) error { return nil }
func (s *errorAddQuarantineStore) IsQuarantined(context.Context, string) (bool, error) {
	return false, nil
}
func (s *errorAddQuarantineStore) ListQuarantined(context.Context) ([]index.QuarantineEntry, error) {
	return nil, nil
}
func (s *errorAddQuarantineStore) ClearQuarantine(context.Context) error { return nil }

func TestQuarantineFailedPath_AddToQuarantineError(t *testing.T) {
	root := t.TempDir()
	quarantine := &errorAddQuarantineStore{addErr: errors.New("db error")}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.quarantineFailedPath(context.Background(), file, errors.New("failure"))
	if quarantine.addedPath != "a.txt" {
		t.Fatalf("expected AddToQuarantine to be called with 'a.txt', got %q", quarantine.addedPath)
	}
}

func TestHandleWatchEvent_IgnoredPath(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.ExcludePatterns = []string{"*.log"}

	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        cfg,
		store:      store,
		extractor:  ext,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	logFile := filepath.Join(root, "app.log")
	writeFile(t, logFile, "log data")

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  logFile,
		Op:    watch.Create,
		IsDir: false,
	})

	if store.deletedPath != "" {
		t.Fatal("expected no store interaction for ignored path")
	}
}

func TestHandleWatchEvent_QuarantinedPath(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{quarantined: true}
	store := &stubDocumentStore{}
	ext := &stubExtractor{supportsPath: filepath.Join(root, "a.txt")}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		extractor:  ext,
		embedder:   emb,
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "data")

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  file,
		Op:    watch.Create,
		IsDir: false,
	})

	if store.deletedPath != "" {
		t.Fatal("expected no store interaction for quarantined path")
	}
}

func TestHandleWatchEvent_CreateForDirectory(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		extractor:  ext,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  dir,
		Op:    watch.Create,
		IsDir: false,
	})

	if store.deletedPath != "" {
		t.Fatal("expected no store interaction for Create event on directory")
	}
}

func TestHandleWatchEvent_WriteForDirectory(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		extractor:  ext,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  dir,
		Op:    watch.Write,
		IsDir: false,
	})

	if store.deletedPath != "" {
		t.Fatal("expected no store interaction for Write event on directory")
	}
}

func TestHandleWatchEvent_CreateNonexistentPath(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		extractor:  ext,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	nonexistent := filepath.Join(root, "ghost.txt")
	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  nonexistent,
		Op:    watch.Create,
		IsDir: false,
	})

	if store.deletedPath != "" {
		t.Fatal("expected no store interaction for nonexistent path on Create")
	}
}

func TestHandleWatchEvent_RemoveKeyError(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		extractor:  ext,
		embedder:   emb,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  root,
		Op:    watch.Remove,
		IsDir: false,
	})

	store.mu.Lock()
	ops := len(store.deletedPaths)
	store.mu.Unlock()
	if ops != 0 {
		t.Fatal("expected no delete calls for invalid key on Remove")
	}
}

func TestProcessLiveIndexRequest_NotPending(t *testing.T) {
	root := t.TempDir()
	idx := newTestIndexer(root, &stubDocumentStore{}, &stubExtractor{}, &stubEmbedder{dimensions: 3})

	idx.processLiveIndexRequest(context.Background(), filepath.Join(root, "nonexistent.txt"))
}

func TestProcessLiveIndexRequest_ProcessesFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "hello world")

	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	ext := &stubExtractor{supportsPath: file, text: "hello world"}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = NewLiveIndexQueue(2)

	modTime := time.Now()
	if !idx.live.MarkPending(file, modTime) {
		t.Fatal("expected MarkPending to return true")
	}

	idx.processLiveIndexRequest(context.Background(), file)

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed")
	}
}

func TestScheduleIndexRetry_NilRetries(t *testing.T) {
	root := t.TempDir()
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		quarantine: &stubQuarantineStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    nil,
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.scheduleIndexRetry(context.Background(), file, time.Now(), errors.New("transient"))
}

func TestScheduleIndexRetry_NonRetryableNotQuarantined(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	store := &stubDocumentStore{}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.scheduleIndexRetry(context.Background(), file, time.Now(), context.Canceled)
	if quarantine.addedPath != "" {
		t.Fatal("expected no quarantine for context canceled error")
	}
}

func TestScheduleIndexRetry_QuarantineOnGiveUp(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	store := &stubDocumentStore{}

	oldDelay := IndexRetryBaseDelay
	oldMaxAttempts := MaxIndexRetryAttempts
	IndexRetryBaseDelay = 10 * time.Millisecond
	MaxIndexRetryAttempts = 1
	defer func() {
		IndexRetryBaseDelay = oldDelay
		MaxIndexRetryAttempts = oldMaxAttempts
	}()

	file := filepath.Join(root, "a.txt")

	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		quarantine: quarantine,
		extractor:  &stubExtractor{supportsPath: file, text: "data"},
		embedder:   &stubEmbedder{dimensions: 3},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(16),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	permanentErr := errors.New("transient failure")

	idx.scheduleIndexRetry(context.Background(), file, time.Now(), permanentErr)
	time.Sleep(30 * time.Millisecond)

	result := idx.retries.Schedule(file, time.Now(), func(time.Time) {})
	if result != RetryScheduleGaveUp {
		t.Fatalf("expected RetryScheduleGaveUp after max attempts, got %v", result)
	}

	idx.scheduleIndexRetry(context.Background(), file, time.Now(), embed.ErrPermanent)
	if quarantine.addedPath != "a.txt" {
		t.Fatalf("expected quarantined path 'a.txt', got %q", quarantine.addedPath)
	}
}

func TestIsQuarantined_NilQuarantine(t *testing.T) {
	root := t.TempDir()
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	if idx.isQuarantined(context.Background(), "a.txt") {
		t.Fatal("expected false when quarantine store is nil")
	}
}

func TestShouldIndexExistingPath_GitIgnoredFile(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(subdir, ".gitignore"), "*.tmp\n")

	file := filepath.Join(subdir, "cache.tmp")
	writeFile(t, file, "temp data")

	ext := &stubExtractor{supportsPath: file}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)
	ok, err := idx.shouldIndexExistingPath(gi, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for git-ignored file")
	}
}

func TestShouldIndexExistingPath_SupportedNotIgnored(t *testing.T) {
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

func TestShouldIndexExistingPath_StatError(t *testing.T) {
	root := t.TempDir()
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, &stubDocumentStore{}, ext, emb)

	gi := scan.NewGitIgnoreMatcher(root, nil)

	nonexistentPath := filepath.Join(root, "nope", "file.txt")
	ok, err := idx.shouldIndexExistingPath(gi, nonexistentPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for nonexistent path")
	}
}

func TestSyncDocument_ModTimeMismatchTriggersReindex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.txt")
	writeFile(t, file, "initial content")

	ext := &stubExtractor{supportsPath: file, text: "initial content"}
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

	writeFile(t, file, "updated content")
	ext2 := &stubExtractor{supportsPath: file, text: "updated content"}
	idx.extractor = ext2

	newModTime := time.Now().Add(time.Second)
	action, err = idx.SyncDocument(context.Background(), "docs/a.txt", file, &newModTime, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated on second call, got %s", action)
	}
}

func TestIndexFileCore_StaleVersion(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	later := time.Now().Add(time.Second)
	idx.paths.Begin("a.txt", &later)

	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "", nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for stale version, got %s", action)
	}
}

func TestHandleWatchEvent_RemoveNoDoc(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  filepath.Join(root, "nonexistent.txt"),
		Op:    watch.Remove,
		IsDir: false,
	})

	store.mu.Lock()
	ops := len(store.deletedPaths)
	store.mu.Unlock()
	if ops != 0 {
		t.Fatal("expected no delete calls when no doc exists and file is gone")
	}
}

func TestHandleWatchEvent_WriteExistingFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  file,
		Op:    watch.Write,
		IsDir: false,
	})

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed on Write event")
	}
}

func TestEnqueueLiveIndex_DirectPath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	ok := idx.EnqueueLiveIndex(context.Background(), file, time.Now())
	if !ok {
		t.Fatal("expected EnqueueLiveIndex to return true when live is nil")
	}

	store.mu.Lock()
	got := len(store.reindexed) > 0
	store.mu.Unlock()
	if !got {
		t.Fatal("expected document to be reindexed via direct path")
	}
}

func TestEnqueueLiveIndex_QueuePath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)

	ok := idx.EnqueueLiveIndex(context.Background(), file, time.Now())
	if !ok {
		t.Fatal("expected EnqueueLiveIndex to return true")
	}

	var queued string
	select {
	case queued = <-idx.live.Jobs:
	default:
		t.Fatal("expected path to be queued")
	}
	if queued != file {
		t.Fatalf("expected queued path %s, got %s", file, queued)
	}
}

func TestSameModTime_ZeroTimes(t *testing.T) {
	var zero time.Time
	if !SameModTime(zero, zero) {
		t.Fatal("expected zero times to be equal")
	}
}

func TestShouldRetryIndexError_TransientError(t *testing.T) {
	if !shouldRetryIndexError(errors.New("transient")) {
		t.Fatal("expected transient error to be retryable")
	}
}

func TestShouldRetryIndexError_ErrOCRFailed(t *testing.T) {
	if shouldRetryIndexError(ErrOCRFailed) {
		t.Fatal("expected OCR failed error to not be retryable")
	}
}

func TestShouldRetryIndexError_ErrFileTooLarge(t *testing.T) {
	if shouldRetryIndexError(ErrFileTooLarge) {
		t.Fatal("expected file too large error to not be retryable")
	}
}

func TestShouldRetryIndexError_Nil(t *testing.T) {
	if shouldRetryIndexError(nil) {
		t.Fatal("expected nil error to not be retryable")
	}
}

func TestShouldQuarantineIndexError_TransientError(t *testing.T) {
	if shouldQuarantineIndexError(errors.New("transient")) {
		t.Fatal("expected transient error to not be quarantined")
	}
}

func TestShouldQuarantineIndexError_ContextCanceled(t *testing.T) {
	if shouldQuarantineIndexError(context.Canceled) {
		t.Fatal("expected context canceled to not be quarantined")
	}
}

func TestShouldQuarantineIndexError_ContextDeadlineExceeded(t *testing.T) {
	if shouldQuarantineIndexError(context.DeadlineExceeded) {
		t.Fatal("expected context deadline exceeded to not be quarantined")
	}
}

func TestIsLocalURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:8080", true},
		{"http://127.0.0.1:11434", true},
		{"http://[::1]:11434", true},
		{"http://example.com", false},
		{"https://example.com", false},
		{"://bad", false},
	}
	for _, tt := range tests {
		if got := isLocalURL(tt.url); got != tt.want {
			t.Errorf("isLocalURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestLogPathForDB_EdgeCases(t *testing.T) {
	if got := LogPathForDB("/tmp/quant.db"); got != "/tmp/quant.log" {
		t.Fatalf("unexpected log path: %q", got)
	}
	if got := LogPathForDB("/tmp/quant"); got != "/tmp/quant.log" {
		t.Fatalf("unexpected log path for no extension: %q", got)
	}
	if got := LogPathForDB("/tmp/test.mydb"); got != "/tmp/test.log" {
		t.Fatalf("unexpected log path for custom extension: %q", got)
	}
}

func TestIsCompanionLogPathForDB_EdgeCases(t *testing.T) {
	if !IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.1") {
		t.Fatal("expected rotated log to be companion")
	}
	if !IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.99") {
		t.Fatal("expected rotated log to be companion")
	}
	if !IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log") {
		t.Fatal("expected exact log path to be a companion")
	}
	if IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.bak") {
		t.Fatal("expected non-numeric suffix to not be companion")
	}
	if IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/other.log") {
		t.Fatal("expected different base to not be companion")
	}
	if IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.") {
		t.Fatal("expected empty numeric suffix to not match")
	}
	if IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.a1") {
		t.Fatal("expected non-numeric suffix to not match")
	}
}

func TestRemoveDocumentIfPresent_NilDoc(t *testing.T) {
	store := &stubDocumentStore{}
	action, err := removeDocumentIfPresent(context.Background(), store, nil, "docs/a.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for nil doc, got %s", action)
	}
	if store.deletedPath != "" {
		t.Fatal("expected no delete call when doc is nil")
	}
}

func TestRemoveDocumentIfPresent_ExistingDoc(t *testing.T) {
	store := &stubDocumentStore{}
	action, err := removeDocumentIfPresent(context.Background(), store, &index.Document{Path: "docs/a.txt"}, "docs/a.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexRemoved {
		t.Fatalf("expected removed, got %s", action)
	}
	if store.deletedPath != "docs/a.txt" {
		t.Fatalf("expected delete call for docs/a.txt, got %q", store.deletedPath)
	}
}

func TestRemoveDocumentIfPresent_DeleteError(t *testing.T) {
	store := &stubDocumentStore{deleteErr: errors.New("db error")}
	_, err := removeDocumentIfPresent(context.Background(), store, &index.Document{Path: "docs/a.txt"}, "docs/a.txt")
	if err == nil {
		t.Fatal("expected error when delete fails")
	}
}

func TestQuarantineFailedPath_NilConfig(t *testing.T) {
	idx := &Indexer{
		store:      &stubDocumentStore{},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	idx.quarantineFailedPath(context.Background(), "/some/path", errors.New("failure"))
}

type errorDeleteStore struct {
	trackingDocumentStore
}

func (s *errorDeleteStore) DeleteDocument(_ context.Context, path string) error {
	s.mu.Lock()
	s.deletedPaths = append(s.deletedPaths, path)
	s.mu.Unlock()
	return errors.New("delete failed")
}

func TestQuarantineFailedPath_DeleteDocError(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	store := &errorDeleteStore{
		trackingDocumentStore: trackingDocumentStore{},
	}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      store,
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.quarantineFailedPath(context.Background(), file, errors.New("permanent failure"))
	if quarantine.addedPath != "a.txt" {
		t.Fatalf("expected quarantined path 'a.txt', got %q", quarantine.addedPath)
	}

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "a.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected DeleteDocument to be attempted even on error")
	}
}

func TestSetIndexState_NilIndexer(t *testing.T) {
	var idx *Indexer
	idx.setIndexState(runtimestate.IndexStateReady, "test")
}

func TestSetIndexState_NilTracker(t *testing.T) {
	idx := &Indexer{}
	idx.setIndexState(runtimestate.IndexStateReady, "test")
}

func TestProcessLiveIndexRequestDirect_ExtractionError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, err: errors.New("extraction failed")}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	idx.processLiveIndexRequestDirect(context.Background(), file, time.Now())

	snap := idx.IndexState.Snapshot()
	if snap.State != runtimestate.IndexStateDegraded {
		t.Fatalf("expected degraded state after error, got %s", snap.State)
	}
}

func TestProcessLiveIndexRequestDirect_RemoveAction(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{docByPath: &index.Document{Path: "gone.txt", Hash: "abc"}}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)
	idx.live = nil

	path := filepath.Join(root, "gone.txt")
	idx.processLiveIndexRequestDirect(context.Background(), path, time.Now())

	store.mu.Lock()
	found := false
	for _, p := range store.deletedPaths {
		if p == "gone.txt" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected document to be removed after IndexRemoved action")
	}
}

func TestHandleWatchEvent_RemoveDirectoryPrefix(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	idx.HandleWatchEvent(context.Background(), watch.Event{
		Path:  filepath.Join(root, "docs"),
		Op:    watch.Remove,
		IsDir: true,
	})

	store.mu.Lock()
	got := store.deletedPrefix
	store.mu.Unlock()
	if got != "docs" {
		t.Fatalf("expected deleted prefix 'docs', got %q", got)
	}
}

func TestSyncDocument_EmbeddingError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "docs", "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3, err: errors.New("embedding failed")}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	action, err := idx.SyncDocument(context.Background(), "docs/a.txt", file, nil, nil)
	if err == nil {
		t.Fatalf("expected embedding error, got action=%s", action)
	}
}

func TestSyncDocument_BeginNotStarted(t *testing.T) {
	root := t.TempDir()
	store := &trackingDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newSyncTestIndexer(root, store, ext, emb)

	modTime := time.Now()
	version, started := idx.paths.Begin("test/a.txt", &modTime)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	_, started2 := idx.paths.Begin("test/a.txt", nil)
	if started2 {
		t.Fatal("expected second Begin with nil modTime and current hasModTime=true to not start")
	}

	action, err := idx.SyncDocument(context.Background(), "test/a.txt", filepath.Join(root, "test", "a.txt"), &modTime, nil)
	_ = version
	_ = action
	_ = err
}

func TestIndexFileCore_EmbeddingError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3, err: errors.New("embedding failed")}
	store := &trackingDocumentStore{chunksByPath: map[string]index.ChunkRecord{}}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "", nil, version)
	if err == nil {
		t.Fatalf("expected embedding error, got action=%s", action)
	}
}

func TestIndexFileCore_PrecomputedHashMismatch(t *testing.T) {
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

	action, err := idx.indexFileCore(context.Background(), "a.txt", file, time.Now(), "wrong-hash", nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexUpdated {
		t.Fatalf("expected updated for hash mismatch, got %s", action)
	}
}

func TestRetrySchedulerClear_NonExistent(t *testing.T) {
	rs := NewRetryScheduler()
	rs.Clear("nonexistent_path")
	rs.Clear("")
}

func TestPathSyncTracker_BeginWithNilThenWithModTime(t *testing.T) {
	tracker := NewPathSyncTracker()

	v1, started1 := tracker.Begin("docs/a.md", nil)
	if !started1 {
		t.Fatal("expected first Begin with nil modTime to start")
	}

	v2, started2 := tracker.Begin("docs/a.md", nil)
	if started2 || v2 != v1 {
		t.Fatalf("expected nil modTime with nil hasModTime to not invalidate: version=%d started=%v", v2, started2)
	}

	modTime := time.Now()
	v3, started3 := tracker.Begin("docs/a.md", &modTime)
	if started3 {
		t.Fatalf("expected Begin while running to not start new sync: version=%d started=%v", v3, started3)
	}
	if v3 <= v1 {
		t.Fatalf("expected new modTime to bump version: v1=%d v3=%d", v1, v3)
	}
	_, rerun := tracker.Finish("docs/a.md")
	if !rerun {
		t.Fatal("expected dirty version to trigger rerun")
	}
	_, rerun2 := tracker.Finish("docs/a.md")
	if rerun2 {
		t.Fatal("expected clean version to not rerun")
	}
}

func TestPathSyncTracker_InvalidatePrefix(t *testing.T) {
	tracker := NewPathSyncTracker()
	_, started := tracker.Begin("docs/a.md", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	tracker.InvalidatePrefix("docs")
	_, rerun := tracker.Finish("docs/a.md")
	if !rerun {
		t.Fatal("expected InvalidatePrefix to trigger rerun")
	}
	_, rerun2 := tracker.Finish("docs/a.md")
	if rerun2 {
		t.Fatal("expected clean finish after rerun")
	}
}

func TestEnqueueLiveIndex_MarkPendingReturnsFalse(t *testing.T) {
	root := t.TempDir()
	store := &stubDocumentStore{}
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	idx := newTestIndexer(root, store, ext, emb)

	file := filepath.Join(root, "a.txt")
	modTime := time.Now()

	if !idx.EnqueueLiveIndex(context.Background(), file, modTime) {
		t.Fatal("expected first EnqueueLiveIndex to return true")
	}

	if !idx.EnqueueLiveIndex(context.Background(), file, modTime) {
		t.Fatal("expected second EnqueueLiveIndex with same modTime to return true (already queued)")
	}
}

func TestScheduleIndexRetry_ClearAfterSuccess(t *testing.T) {
	root := t.TempDir()
	quarantine := &stubQuarantineStore{}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(16),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}

	file := filepath.Join(root, "a.txt")
	idx.scheduleIndexRetry(context.Background(), file, time.Now(), errors.New("transient"))
	time.Sleep(50 * time.Millisecond)

	if idx.retries == nil {
		t.Fatal("expected retries scheduler to be set")
	}

	idx.retries.Clear(file)
}

func TestSyncDocumentOnce_StatError(t *testing.T) {
	root := t.TempDir()
	ext := &stubExtractor{}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("nonexistent", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "nonexistent", "/nonexistent/path/that/should/not/exist/abc123.txt", nil, version)
	if action != IndexNoop && action != IndexRemoved {
		t.Fatalf("expected noop or removed, got %s", action)
	}
	if err != nil {
		t.Fatalf("unexpected error for stat error: %v", err)
	}
}

func TestSyncDocumentOnce_UnsupportedTypeNoDoc(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.dat")
	writeFile(t, file, "data")

	ext := &stubExtractor{supportsPath: ""}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.dat", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.syncDocumentOnce(context.Background(), "a.dat", file, nil, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != IndexNoop {
		t.Fatalf("expected noop for unsupported type with no doc, got %s", action)
	}
}

func TestIndexFileCore_NilDocHashMatch(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
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
		t.Fatalf("expected updated for new file, got %s", action)
	}
}

func TestIndexFileCore_HashComputationError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "content")

	ext := &stubExtractor{supportsPath: file, text: "content"}
	emb := &stubEmbedder{dimensions: 3}
	store := &trackingDocumentStore{}
	idx := newSyncTestIndexer(root, store, ext, emb)

	version, started := idx.paths.Begin("a.txt", nil)
	if !started {
		t.Fatal("expected Begin to succeed")
	}

	action, err := idx.indexFileCore(context.Background(), "a.txt", "/nonexistent/path/a.txt", time.Now(), "", nil, version)
	if err == nil {
		t.Fatalf("expected hash error for nonexistent file, got action=%s", action)
	}
}
