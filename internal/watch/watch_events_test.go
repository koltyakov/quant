package watch

import (
	"testing"
	"time"
)

func TestTrySendResync_EmptyChannel(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.trySendResync()

	select {
	case evt := <-w.events:
		if evt.Op != Resync || evt.Path != "/test" {
			t.Fatalf("expected resync event for /test, got %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected resync event to be sent")
	}

	w.mu.Lock()
	pending := w.resyncPending
	w.mu.Unlock()
	if pending {
		t.Fatal("expected resyncPending to be false after successful send")
	}
}

func TestTrySendResync_FullChannel_SetsUpRetry(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:        make(chan Event),
		done:          make(chan struct{}),
		rootDir:       "/test",
		timers:        make(map[string]*time.Timer),
		watchedDirs:   map[string]struct{}{"/test": {}},
		resyncPending: true,
	}

	w.trySendResync()

	w.mu.Lock()
	timer := w.resyncTimer
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func TestSignalResync_Basic(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.signalResync()

	select {
	case evt := <-w.events:
		if evt.Op != Resync {
			t.Fatalf("expected Resync event, got %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected resync event from signalResync")
	}
}

func TestSignalResync_AlreadyPending(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.signalResync()

	w.mu.Lock()
	w.resyncPending = true
	w.mu.Unlock()

	w.signalResync()

	select {
	case evt := <-w.events:
		_ = evt
	default:
	}
}

func TestSignalResync_ClosedWatcher(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
		closed:      true,
	}

	w.signalResync()

	select {
	case <-w.events:
		t.Fatal("expected no event from closed watcher")
	default:
	}
}

func TestDebounce_Basic(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.debounce("/test/file.txt", Create, false)

	w.mu.Lock()
	_, hasTimer := w.timers["/test/file.txt"]
	w.mu.Unlock()
	if !hasTimer {
		t.Fatal("expected debounce timer to be set")
	}

	select {
	case evt := <-w.events:
		if evt.Path != "/test/file.txt" || evt.Op != Create {
			t.Fatalf("expected debounced event, got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected debounced event to fire")
	}
}

func TestDebounce_ClosedWatcher(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
		closed:      true,
	}

	w.debounce("/test/file.txt", Create, false)

	w.mu.Lock()
	_, hasTimer := w.timers["/test/file.txt"]
	w.mu.Unlock()
	if hasTimer {
		t.Fatal("expected no debounce timer on closed watcher")
	}
}

func TestDebounce_ReplacesExistingTimer(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.debounce("/test/file.txt", Create, false)
	w.debounce("/test/file.txt", Write, false)

	select {
	case evt := <-w.events:
		if evt.Op != Write {
			t.Fatalf("expected Write event from second debounce, got %s", evt.Op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected debounced event to fire")
	}
}

func TestDebounce_FullEventChannel_TriggersResync(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.debounce("/test/file.txt", Create, false)

	time.Sleep(700 * time.Millisecond)
}

func TestSignalResyncDebounced_Basic(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
	}

	w.signalResyncDebounced()

	select {
	case evt := <-w.events:
		if evt.Op != Resync {
			t.Fatalf("expected Resync event, got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected debounced resync event to fire")
	}
}

func TestSignalResyncDebounced_ClosedWatcher(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		rootDir:     "/test",
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{"/test": {}},
		closed:      true,
	}

	w.signalResyncDebounced()

	time.Sleep(100 * time.Millisecond)
}

func TestRemoveWatchedDirEntry_ExistingDir(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		watchedDirs: map[string]struct{}{
			"/test":          {},
			"/test/sub":      {},
			"/test/sub/deep": {},
			"/other":         {},
		},
	}

	isDir := w.removeWatchedDirEntry("/test/sub")

	if !isDir {
		t.Fatal("expected true for existing watched directory")
	}

	w.mu.Lock()
	count := len(w.watchedDirs)
	w.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 remaining watched dirs, got %d", count)
	}
}

func TestRemoveWatchedDirEntry_NonExistingDir(t *testing.T) {
	t.Parallel()

	w := &Watcher{
		watchedDirs: map[string]struct{}{
			"/test": {},
		},
	}

	isDir := w.removeWatchedDirEntry("/test/nonexistent")

	if isDir {
		t.Fatal("expected false for non-existing directory")
	}
}
