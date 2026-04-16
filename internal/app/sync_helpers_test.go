package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

func TestResyncCoordinatorInitialSyncAndPendingResync(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var mu sync.Mutex
	var states []string
	var onReadyCalled int
	var startupCalls int
	var resyncCalls int
	blockResync := make(chan struct{})
	resyncStarted := make(chan struct{}, 1)

	coordinator := NewResyncCoordinator(ResyncCallbacks{
		OnStartup: func(context.Context) (SyncReport, error) {
			mu.Lock()
			startupCalls++
			mu.Unlock()
			return SyncReport{}, nil
		},
		OnResync: func(context.Context) (SyncReport, error) {
			mu.Lock()
			resyncCalls++
			calls := resyncCalls
			mu.Unlock()
			select {
			case resyncStarted <- struct{}{}:
			default:
			}
			<-blockResync
			if calls == 1 {
				return SyncReport{HadIndexFailures: true}, nil
			}
			return SyncReport{}, nil
		},
		OnState: func(state runtimestate.IndexState, message string) {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, string(state)+":"+message)
		},
		OnReady: func(context.Context, SyncReport) {
			mu.Lock()
			onReadyCalled++
			mu.Unlock()
		},
	})

	coordinator.RunInitialSync(ctx)
	mu.Lock()
	su, rd := startupCalls, onReadyCalled
	mu.Unlock()
	if su != 1 || rd != 1 {
		t.Fatalf("unexpected initial sync callbacks: startup=%d ready=%d", su, rd)
	}

	coordinator.RequestResync(ctx)
	<-resyncStarted
	coordinator.RequestResync(ctx)
	close(blockResync)

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		rc := resyncCalls
		mu.Unlock()
		if rc >= 2 {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			rc = resyncCalls
			mu.Unlock()
			t.Fatalf("timed out waiting for coalesced resyncs, got %d calls", rc)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	gotStates := append([]string(nil), states...)
	mu.Unlock()
	if !slices.Contains(gotStates, string(runtimestate.IndexStateReady)+":initial scan complete") {
		t.Fatalf("missing initial ready state in %v", gotStates)
	}
	if !slices.Contains(gotStates, string(runtimestate.IndexStateDegraded)+":filesystem resync completed with indexing failures") {
		t.Fatalf("missing degraded state in %v", gotStates)
	}
	if !slices.Contains(gotStates, string(runtimestate.IndexStateReady)+":filesystem resync complete") {
		t.Fatalf("missing ready resync state in %v", gotStates)
	}
}

func TestResyncCoordinatorErrorAndCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stateCalled := false
	coordinator := NewResyncCoordinator(ResyncCallbacks{
		OnStartup: func(context.Context) (SyncReport, error) {
			return SyncReport{}, context.Canceled
		},
		OnState: func(runtimestate.IndexState, string) {
			stateCalled = true
		},
	})
	coordinator.RunInitialSync(ctx)
	if stateCalled {
		t.Fatal("canceled initial sync should not emit degraded state")
	}
	if coordinator.finish(false) {
		t.Fatal("finish should not request retry once coordinator is idle")
	}
}

func TestPathSyncTrackerLiveQueueAndRetryScheduler(t *testing.T) {
	mod1 := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	mod2 := mod1.Add(time.Second)

	tracker := NewPathSyncTracker()
	version, started := tracker.Begin("docs/a.md", &mod1)
	if !started || version != 1 {
		t.Fatalf("unexpected first Begin result: version=%d started=%v", version, started)
	}
	if !tracker.IsCurrent("docs/a.md", version) {
		t.Fatal("expected version to be current")
	}
	version2, started2 := tracker.Begin("docs/a.md", &mod1)
	if started2 || version2 != version {
		t.Fatalf("same mod time should not invalidate running sync: version=%d started=%v", version2, started2)
	}
	version3, started3 := tracker.Begin("docs/a.md", &mod2)
	if started3 || version3 <= version2 {
		t.Fatalf("newer mod time should mark dirty and bump version: version=%d started=%v", version3, started3)
	}
	if nextVersion, retry := tracker.Finish("docs/a.md"); !retry || nextVersion != version3 {
		t.Fatalf("unexpected Finish retry result: version=%d retry=%v", nextVersion, retry)
	}
	tracker.InvalidatePrefix("docs")
	if nextVersion, retry := tracker.Finish("docs/a.md"); !retry || nextVersion == 0 {
		t.Fatalf("expected invalidated running sync to require one more pass: version=%d retry=%v", nextVersion, retry)
	}
	if nextVersion, retry := tracker.Finish("docs/a.md"); retry || nextVersion != 0 {
		t.Fatalf("expected clean finish after final pass: version=%d retry=%v", nextVersion, retry)
	}

	queue := NewLiveIndexQueue(2)
	if !queue.MarkPending("docs/a.md", mod1) {
		t.Fatal("first pending item should be queueable")
	}
	if queue.MarkPending("docs/a.md", mod2) {
		t.Fatal("duplicate pending item should not enqueue twice")
	}
	startMod, ok := queue.StartProcessing("docs/a.md")
	if !ok || !startMod.Equal(mod2) {
		t.Fatalf("unexpected StartProcessing result: mod=%v ok=%v", startMod, ok)
	}
	if queue.MarkPending("docs/a.md", mod2.Add(time.Second)) {
		t.Fatal("running item should stay coalesced instead of enqueueing again")
	}
	if !queue.FinishProcessing("docs/a.md") {
		t.Fatal("expected pending work to be requeued after finish")
	}
	queue.Cancel("docs/a.md")
	if _, ok := queue.StartProcessing("docs/a.md"); ok {
		t.Fatal("cancel should drop queued work before processing starts")
	}
	if queue.FinishProcessing("docs/a.md") {
		t.Fatal("expected canceled path to remain cleared")
	}

	oldDelay := IndexRetryBaseDelay
	oldMaxAttempts := MaxIndexRetryAttempts
	IndexRetryBaseDelay = 10 * time.Millisecond
	MaxIndexRetryAttempts = 2
	defer func() {
		IndexRetryBaseDelay = oldDelay
		MaxIndexRetryAttempts = oldMaxAttempts
	}()

	retries := NewRetryScheduler()
	if got := retries.Schedule("", mod1, func(time.Time) {}); got != RetryScheduleIgnored {
		t.Fatalf("unexpected empty path retry result: %v", got)
	}

	fired := make(chan time.Time, 1)
	if got := retries.Schedule("docs/a.md", mod1, func(retryModTime time.Time) {
		fired <- retryModTime
	}); got != RetryScheduleScheduled {
		t.Fatalf("unexpected initial retry result: %v", got)
	}
	if got := retries.Schedule("docs/a.md", mod2, func(time.Time) {}); got != RetrySchedulePending {
		t.Fatalf("unexpected pending retry result: %v", got)
	}
	select {
	case firedMod := <-fired:
		if !firedMod.Equal(mod2) {
			t.Fatalf("expected latest mod time on fire, got %v", firedMod)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for retry callback")
	}
	if got := retries.Schedule("docs/a.md", mod2, func(time.Time) {}); got != RetryScheduleScheduled {
		t.Fatalf("unexpected second retry result: %v", got)
	}
	if got := retries.Schedule("docs/a.md", mod2, func(time.Time) {}); got != RetrySchedulePending {
		t.Fatalf("unexpected pending state before give-up: %v", got)
	}
	time.Sleep(30 * time.Millisecond)
	if got := retries.Schedule("docs/a.md", mod2, func(time.Time) {}); got != RetryScheduleGaveUp {
		t.Fatalf("unexpected give-up result: %v", got)
	}
	retries.Clear("docs/a.md")
}

func TestHelperFunctions(t *testing.T) {
	t.Parallel()

	if got := LogPathForDB("/tmp/quant.db"); got != "/tmp/quant.log" {
		t.Fatalf("unexpected log path: %q", got)
	}
	if got := LogPathForDB("/tmp/quant"); got != "/tmp/quant.log" {
		t.Fatalf("unexpected extensionless log path: %q", got)
	}
	if !IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.2") {
		t.Fatal("expected rotated companion log to match")
	}
	if IsCompanionLogPathForDB("/tmp/quant.db", "/tmp/quant.log.bak") {
		t.Fatal("non-numeric suffix should not match companion log pattern")
	}

	for _, rawURL := range []string{"http://localhost:11434", "http://127.0.0.1:8080", "http://[::1]:11434"} {
		if !isLocalURL(rawURL) {
			t.Fatalf("expected local URL: %q", rawURL)
		}
	}
	for _, rawURL := range []string{"http://example.com", "://bad"} {
		if isLocalURL(rawURL) {
			t.Fatalf("expected non-local URL: %q", rawURL)
		}
	}

	if got := LiveQueueSizeForWorkers(0); got != 16 {
		t.Fatalf("unexpected min live queue size: %d", got)
	}
	if got := LiveQueueSizeForWorkers(100); got != 512 {
		t.Fatalf("unexpected capped live queue size: %d", got)
	}

	if !reflect.DeepEqual(pathSyncTrackerSnapshot(NewPathSyncTracker()), map[string]uint64{}) {
		t.Fatal("expected empty path tracker snapshot")
	}
}

func TestIndexerHelperBranches(t *testing.T) {
	root := t.TempDir()
	path := root + "/docs/a.md"
	if key, err := DocumentKey(root, path); err != nil || key != "docs/a.md" {
		t.Fatalf("unexpected document key: key=%q err=%v", key, err)
	}
	if _, err := DocumentKey(root, root); err == nil {
		t.Fatal("expected watch root to be rejected as a document key")
	}
	if _, err := DocumentKey(root, root+"/../outside.md"); err == nil {
		t.Fatal("expected outside-root path to be rejected")
	}
	if normalized, err := NormalizeStoredDocumentPath(root, "docs/a.md"); err != nil || normalized != "docs/a.md" {
		t.Fatalf("unexpected normalized path: %q err=%v", normalized, err)
	}

	for _, tc := range []struct {
		err        error
		retry      bool
		quarantine bool
	}{
		{nil, false, false},
		{context.Canceled, false, false},
		{ErrOCRFailed, false, true},
		{ErrFileTooLarge, false, true},
		{embed.ErrPermanent, false, true},
		{errors.New("transient"), true, false},
	} {
		if got := shouldRetryIndexError(tc.err); got != tc.retry {
			t.Fatalf("shouldRetryIndexError(%v) = %v want %v", tc.err, got, tc.retry)
		}
		if got := shouldQuarantineIndexError(tc.err); got != tc.quarantine {
			t.Fatalf("shouldQuarantineIndexError(%v) = %v want %v", tc.err, got, tc.quarantine)
		}
	}

	docStore := &stubDocumentStore{}
	if action, err := removeDocumentIfPresent(context.Background(), docStore, nil, "docs/a.md"); err != nil || action != IndexNoop {
		t.Fatalf("unexpected noop remove result: action=%s err=%v", action, err)
	}
	if action, err := removeDocumentIfPresent(context.Background(), docStore, &index.Document{Path: "docs/a.md"}, "docs/a.md"); err != nil || action != IndexRemoved || docStore.deletedPath != "docs/a.md" {
		t.Fatalf("unexpected delete result: action=%s err=%v path=%q", action, err, docStore.deletedPath)
	}
	docStore.deleteErr = errors.New("delete failed")
	if _, err := removeDocumentIfPresent(context.Background(), docStore, &index.Document{Path: "docs/a.md"}, "docs/a.md"); err == nil {
		t.Fatal("expected wrapped delete error")
	}

	quarantine := &stubQuarantineStore{}
	idx := &Indexer{
		cfg:        &config.Config{WatchDir: root},
		store:      &stubDocumentStore{},
		quarantine: quarantine,
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(2),
		retries:    NewRetryScheduler(),
	}

	oldDelay := IndexRetryBaseDelay
	oldMaxAttempts := MaxIndexRetryAttempts
	IndexRetryBaseDelay = 10 * time.Millisecond
	MaxIndexRetryAttempts = 1
	defer func() {
		IndexRetryBaseDelay = oldDelay
		MaxIndexRetryAttempts = oldMaxAttempts
	}()

	idx.scheduleIndexRetry(context.Background(), path, time.Now(), ErrFileTooLarge)
	if quarantine.addedPath != "docs/a.md" {
		t.Fatalf("expected path to be quarantined, got %q", quarantine.addedPath)
	}

	idx.retries = NewRetryScheduler()
	idx.scheduleIndexRetry(context.Background(), path, time.Now(), errors.New("temporary"))
	time.Sleep(30 * time.Millisecond)
	if len(idx.live.Jobs) != 1 {
		t.Fatal("expected transient retry to enqueue work")
	}
}

func TestIndexerConstructionAndSmallHelpers(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.IndexWorkers = 3
	cfg.ChunkSize = 256
	cfg.ChunkOverlap = 0.2
	cfg.EmbedBatchSize = 4
	cfg.IncludePatterns = []string{"**/*.go"}
	cfg.ExcludePatterns = []string{"vendor/**"}

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})
	if idx == nil || idx.live == nil || idx.paths == nil || idx.retries == nil || idx.IndexState == nil {
		t.Fatal("expected NewIndexer to initialize helper components")
	}
	if idx.pipeline == nil || idx.pipeline.ChunkSize != 256 || idx.pipeline.BatchSize != 4 {
		t.Fatalf("unexpected pipeline config: %+v", idx.pipeline)
	}
	if workers := idx.workerCount(2); workers != 2 {
		t.Fatalf("unexpected worker count cap: %d", workers)
	}
	if queueSize := idx.liveQueueSize(); queueSize != LiveQueueSizeForWorkers(3) {
		t.Fatalf("unexpected live queue size: %d", queueSize)
	}

	if idx.shouldIgnorePath(filepath.Join(root, "main.go")) {
		t.Fatal("expected included Go file to be indexed")
	}
	if !idx.shouldIgnorePath(filepath.Join(root, "README.md")) {
		t.Fatal("expected non-Go file to be ignored by matcher")
	}
	if !idx.shouldIgnorePath(LogPathForDB(cfg.DBPath)) {
		t.Fatal("expected companion log path to be ignored")
	}
	if (&Indexer{}).shouldIgnorePath(filepath.Join(root, "main.go")) {
		t.Fatal("nil config should not ignore paths")
	}

	quarantine := &stubQuarantineStore{quarantined: true}
	idx.quarantine = quarantine
	if !idx.isQuarantined(context.Background(), "docs/a.md") {
		t.Fatal("expected quarantine lookup to be true")
	}
	quarantine.isErr = errors.New("lookup failed")
	if idx.isQuarantined(context.Background(), "docs/a.md") {
		t.Fatal("quarantine lookup errors should fall back to false")
	}

	adapter := newSummarizerAdapter(&index.ChunkSummarizer{})
	if adapter == nil || adapter.inner == nil {
		t.Fatal("expected summarizer adapter to wrap inner summarizer")
	}
}

func TestIndexerThinControlPathsAndSummarizerAdapter(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "quant.db")
	cfg.IndexWorkers = 1

	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      &stubDocumentStore{},
		Quarantine: &stubQuarantineStore{},
	})

	idx.SetIndexState(runtimestate.IndexStateIndexing, "building")
	snap := idx.IndexState.Snapshot()
	if snap.State != runtimestate.IndexStateIndexing || snap.Message != "building" {
		t.Fatalf("unexpected index state snapshot: %+v", snap)
	}
	if idx.LiveQueue() == nil {
		t.Fatal("expected live queue accessor to return queue")
	}
	if id := generateInstanceID(); id == "" || !strings.Contains(id, "-") {
		t.Fatalf("unexpected instance id format: %q", id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	idx.live = nil
	idx.StartLiveIndexWorkers(ctx, &wg)
	cancel()
	wg.Wait()
	if idx.live == nil {
		t.Fatal("expected StartLiveIndexWorkers to initialize live queue")
	}

	idx.live = NewLiveIndexQueue(1)
	idx.live.Jobs <- "already-full"
	idx.Resync = NewResyncCoordinator(ResyncCallbacks{OnResync: func(context.Context) (SyncReport, error) { return SyncReport{}, nil }})
	if ok := idx.EnqueueLiveIndex(context.Background(), filepath.Join(root, "docs/a.md"), time.Now()); ok {
		t.Fatal("expected queue overflow path to return false")
	}
	if snap := idx.IndexState.Snapshot(); snap.State != runtimestate.IndexStateDegraded {
		t.Fatalf("expected degraded state after overflow, got %+v", snap)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		resp.Message.Content = `{"summary":"brief","topics":["topic"]}`
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	adapter := newSummarizerAdapter(index.NewChunkSummarizer(index.SummarizerConfig{
		BaseURL: server.URL,
		Model:   "mini",
		Timeout: time.Second,
	}))
	summaries, err := adapter.SummarizeBatch(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("SummarizeBatch returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Summary != "brief" || !reflect.DeepEqual(summaries[0].Topics, []string{"topic"}) {
		t.Fatalf("unexpected adapter summaries: %+v", summaries)
	}
}

func pathSyncTrackerSnapshot(tk *PathSyncTracker) map[string]uint64 {
	tk.mu.Lock()
	defer tk.mu.Unlock()

	snapshot := make(map[string]uint64, len(tk.states))
	for key, state := range tk.states {
		snapshot[key] = state.version
	}
	return snapshot
}

type stubDocumentStore struct {
	deletedPath string
	deleteErr   error
}

func (s *stubDocumentStore) ReindexDocument(context.Context, *index.Document, []index.ChunkRecord) error {
	return nil
}

func (s *stubDocumentStore) DeleteDocument(_ context.Context, path string) error {
	s.deletedPath = path
	return s.deleteErr
}

func (s *stubDocumentStore) DeleteDocumentsByPrefix(context.Context, string) error { return nil }
func (s *stubDocumentStore) RenameDocumentPath(context.Context, string, string) error {
	return nil
}
func (s *stubDocumentStore) GetDocumentByPath(context.Context, string) (*index.Document, error) {
	return nil, nil
}
func (s *stubDocumentStore) ListDocuments(context.Context) ([]index.Document, error) { return nil, nil }
func (s *stubDocumentStore) ListDocumentsLimit(context.Context, int) ([]index.Document, error) {
	return nil, nil
}
func (s *stubDocumentStore) GetChunkByID(context.Context, int64) (*index.SearchResult, error) {
	return nil, nil
}
func (s *stubDocumentStore) GetDocumentChunksByPath(context.Context, string) (map[string]index.ChunkRecord, error) {
	return nil, nil
}
func (s *stubDocumentStore) Stats(context.Context) (int, int, error) { return 0, 0, nil }

type stubQuarantineStore struct {
	addedPath   string
	quarantined bool
	isErr       error
}

func (s *stubQuarantineStore) AddToQuarantine(_ context.Context, path, _ string) error {
	s.addedPath = path
	return nil
}
func (s *stubQuarantineStore) RemoveFromQuarantine(context.Context, string) error { return nil }
func (s *stubQuarantineStore) IsQuarantined(context.Context, string) (bool, error) {
	return s.quarantined, s.isErr
}
func (s *stubQuarantineStore) ListQuarantined(context.Context) ([]index.QuarantineEntry, error) {
	return nil, nil
}
func (s *stubQuarantineStore) ClearQuarantine(context.Context) error { return nil }
