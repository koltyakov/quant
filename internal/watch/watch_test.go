package watch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/scan"
)

func TestWatcher_RespectsNestedGitIgnore(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, ".gitignore"), []byte("*.tmp\n"), 0644); err != nil {
		t.Fatalf("unexpected gitignore write error: %v", err)
	}

	rootIgnore, err := scan.LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("unexpected load gitignore error: %v", err)
	}

	watcher, err := New(dir, rootIgnore)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	keepPath := filepath.Join(subdir, "keep.txt")
	skipPath := filepath.Join(subdir, "skip.tmp")

	if err := os.WriteFile(keepPath, []byte("keep"), 0644); err != nil {
		t.Fatalf("unexpected keep write error: %v", err)
	}
	if err := os.WriteFile(skipPath, []byte("skip"), 0644); err != nil {
		t.Fatalf("unexpected skip write error: %v", err)
	}

	event := waitForPath(t, watcher.Events(), keepPath, 3*time.Second)
	if event.Path != keepPath {
		t.Fatalf("expected keep event for %s, got %+v", keepPath, event)
	}

	ensureNoEventForPath(t, watcher.Events(), skipPath, 1200*time.Millisecond)
}

func TestWatcher_NewUsesConfiguredEventBuffer(t *testing.T) {
	dir := t.TempDir()
	watcher, err := New(dir, nil, Options{EventBuffer: 3})
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	if cap(watcher.events) != 3 {
		t.Fatalf("expected event buffer capacity 3, got %d", cap(watcher.events))
	}
}

func TestWatcher_AddsNewDirectoriesRecursively(t *testing.T) {
	dir := t.TempDir()
	watcher, err := New(dir, nil)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	subdir := filepath.Join(dir, "newdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	time.Sleep(700 * time.Millisecond)

	filePath := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("unexpected file write error: %v", err)
	}

	event := waitForPath(t, watcher.Events(), filePath, 3*time.Second)
	if event.Path != filePath {
		t.Fatalf("expected file event for %s, got %+v", filePath, event)
	}
}

func TestWatcher_PopulatedDirectoryCreationEmitsFileCreates(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "watched")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	watcher, err := New(dir, nil)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	incoming := filepath.Join(parent, "incoming")
	if err := os.MkdirAll(incoming, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("unexpected file write error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(incoming, ".env"), []byte("TOKEN=secret"), 0644); err != nil {
		t.Fatalf("unexpected hidden file write error: %v", err)
	}
	hiddenDir := filepath.Join(incoming, ".hidden")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatalf("unexpected hidden directory error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hiddenDir, "skip.txt"), []byte("skip"), 0644); err != nil {
		t.Fatalf("unexpected hidden directory file error: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(incoming, "file.txt"), filepath.Join(incoming, "link.txt")); err != nil {
			t.Fatalf("unexpected symlink error: %v", err)
		}
	}

	time.Sleep(700 * time.Millisecond)

	if err := os.Rename(incoming, filepath.Join(dir, "incoming")); err != nil {
		t.Fatalf("unexpected rename error: %v", err)
	}

	moved := filepath.Join(dir, "incoming")
	events := waitForPaths(t, watcher.Events(), []string{
		filepath.Join(moved, "file.txt"),
		filepath.Join(moved, ".env"),
	}, 3*time.Second)
	for _, event := range events {
		if event.Op != Create {
			t.Fatalf("expected create event for moved-in file, got %+v", event)
		}
	}
	ensureNoEventForPath(t, watcher.Events(), filepath.Join(moved, ".hidden", "skip.txt"), 1200*time.Millisecond)
	if runtime.GOOS != "windows" {
		ensureNoEventForPath(t, watcher.Events(), filepath.Join(moved, "link.txt"), 1200*time.Millisecond)
	}
}

func TestWatcher_SubtreeCreatesMatchScanTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("secret"), 0644); err != nil {
		t.Fatalf("unexpected hidden file write error: %v", err)
	}
	hiddenDir := filepath.Join(dir, ".hidden")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatalf("unexpected hidden directory error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hiddenDir, "skip.txt"), []byte("skip"), 0644); err != nil {
		t.Fatalf("unexpected hidden directory file error: %v", err)
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0644); err != nil {
		t.Fatalf("unexpected target write error: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("unexpected symlink error: %v", err)
		}
	}

	watcher := &Watcher{
		matcher:     scan.NewGitIgnoreMatcher(dir, nil),
		rootDir:     dir,
		events:      make(chan Event, 4),
		done:        make(chan struct{}),
		timers:      make(map[string]*time.Timer),
		pendingDirs: make(map[string]bool),
	}
	t.Cleanup(func() {
		watcher.mu.Lock()
		watcher.closed = true
		for _, timer := range watcher.timers {
			timer.Stop()
		}
		watcher.mu.Unlock()
		close(watcher.done)
	})

	if err := watcher.emitSubtreeCreates(dir); err != nil {
		t.Fatalf("emitSubtreeCreates() error: %v", err)
	}
	watcher.mu.Lock()
	_, hiddenFileQueued := watcher.timers[filepath.Join(dir, ".env")]
	_, hiddenDirFileQueued := watcher.timers[filepath.Join(hiddenDir, "skip.txt")]
	_, symlinkQueued := watcher.timers[link]
	watcher.mu.Unlock()
	if !hiddenFileQueued {
		t.Fatal("hidden file was not queued")
	}
	if hiddenDirFileQueued {
		t.Fatal("file in hidden directory was queued")
	}
	if runtime.GOOS != "windows" && symlinkQueued {
		t.Fatal("symlink was queued")
	}
}

func TestWatcher_GitIgnoreChangeRequestsResync(t *testing.T) {
	dir := t.TempDir()
	watcher, err := New(dir, nil)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("*.tmp\n"), 0644); err != nil {
		t.Fatalf("unexpected gitignore write error: %v", err)
	}

	event := waitForOp(t, watcher.Events(), Resync, 3*time.Second)
	if event.Path != dir {
		t.Fatalf("expected resync path %s, got %+v", dir, event)
	}
}

func TestWatcher_EmitsEventsForHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	watcher, err := New(dir, nil)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	hiddenPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(hiddenPath, []byte("TOKEN=1"), 0644); err != nil {
		t.Fatalf("unexpected hidden file write error: %v", err)
	}

	event := waitForPath(t, watcher.Events(), hiddenPath, 3*time.Second)
	if event.Path != hiddenPath {
		t.Fatalf("expected hidden file event for %s, got %+v", hiddenPath, event)
	}
}

func TestWatcher_DirectoryRemovalMarksEventAsDirectory(t *testing.T) {
	dir := t.TempDir()
	watcher, err := New(dir, nil)
	if err != nil {
		t.Fatalf("unexpected watcher error: %v", err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	time.Sleep(700 * time.Millisecond)

	if err := os.RemoveAll(subdir); err != nil {
		t.Fatalf("unexpected remove error: %v", err)
	}

	event := waitForPath(t, watcher.Events(), subdir, 3*time.Second)
	if event.Op != Remove {
		t.Fatalf("expected remove event, got %+v", event)
	}
	if !event.IsDir {
		t.Fatalf("expected directory remove event, got %+v", event)
	}
}

func TestWatcher_BackendErrorRequestsResync(t *testing.T) {
	watcher := &Watcher{
		events: make(chan Event, 1),
		done:   make(chan struct{}),
	}

	var buf bytes.Buffer
	logx.SetOutput(&buf)
	t.Cleanup(func() {
		logx.SetOutput(io.Discard)
	})

	watcher.handleBackendError(errors.New("backend failed"))

	event := waitForOp(t, watcher.Events(), Resync, time.Second)
	if event.Op != Resync {
		t.Fatalf("expected resync event, got %+v", event)
	}
	if buf.Len() == 0 {
		t.Fatal("expected backend error to be logged")
	}
}

func TestWatcher_DirectoryAddFailureRequestsResync(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("unexpected watcher creation error: %v", err)
	}
	if err := fsw.Close(); err != nil {
		t.Fatalf("unexpected watcher close error: %v", err)
	}

	watcher := &Watcher{
		fsw:         fsw,
		matcher:     scan.NewGitIgnoreMatcher(dir, nil),
		rootDir:     dir,
		events:      make(chan Event, 1),
		done:        make(chan struct{}),
		timers:      make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{dir: {}},
	}

	var buf bytes.Buffer
	logx.SetOutput(&buf)
	t.Cleanup(func() {
		logx.SetOutput(io.Discard)
	})

	watcher.handleEvent(fsnotify.Event{Name: subdir, Op: fsnotify.Create})

	event := waitForOp(t, watcher.Events(), Resync, time.Second)
	if event.Path != dir {
		t.Fatalf("expected resync path %s, got %+v", dir, event)
	}
	if !strings.Contains(buf.String(), "could not watch directory") {
		t.Fatalf("expected add failure to be logged, got %q", buf.String())
	}
}

func TestWatcher_RetriesPartialDynamicRegistration(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("unexpected watcher creation error: %v", err)
	}
	watcher := &Watcher{
		fsw:         fsw,
		matcher:     scan.NewGitIgnoreMatcher(dir, nil),
		rootDir:     dir,
		events:      make(chan Event, 4),
		done:        make(chan struct{}),
		timers:      make(map[string]*time.Timer),
		pendingDirs: make(map[string]bool),
		watchedDirs: map[string]struct{}{dir: {}},
		retryDirs:   make(map[string]struct{}),
		watchRetry:  make(chan struct{}, 1),
	}
	var attempts atomic.Int32
	watcher.addWatch = func(path string) error {
		attempt := attempts.Add(1)
		if path == subdir && attempt == 1 {
			return errors.New("too many open files")
		}
		return fsw.Add(path)
	}
	go watcher.loop()
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("unexpected watcher close error: %v", err)
		}
	})

	watcher.handleEvent(fsnotify.Event{Name: subdir, Op: fsnotify.Create})

	deadline := time.Now().Add(3 * time.Second)
	for {
		watcher.mu.Lock()
		_, watched := watcher.watchedDirs[subdir]
		watcher.mu.Unlock()
		if watched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dynamic watch was not retried; attempts = %d", attempts.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatalf("watch attempts = %d, want at least 2", attempts.Load())
	}
}

func waitForPath(t *testing.T, events <-chan Event, wantPath string, timeout time.Duration) Event {
	t.Helper()

	deadline := time.After(timeout)
	for {
		select {
		case event := <-events:
			if event.Path == wantPath {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event for %s", wantPath)
		}
	}
}

func waitForPaths(t *testing.T, events <-chan Event, wantPaths []string, timeout time.Duration) map[string]Event {
	t.Helper()

	wanted := make(map[string]struct{}, len(wantPaths))
	for _, path := range wantPaths {
		wanted[path] = struct{}{}
	}
	got := make(map[string]Event, len(wantPaths))
	deadline := time.After(timeout)
	for len(got) < len(wanted) {
		select {
		case event := <-events:
			if _, ok := wanted[event.Path]; ok {
				got[event.Path] = event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events; got %v, want %v", got, wantPaths)
		}
	}
	return got
}

func ensureNoEventForPath(t *testing.T, events <-chan Event, wantPath string, timeout time.Duration) {
	t.Helper()

	deadline := time.After(timeout)
	for {
		select {
		case event := <-events:
			if event.Path == wantPath {
				t.Fatalf("unexpected event for %s: %+v", wantPath, event)
			}
		case <-deadline:
			return
		}
	}
}

func waitForOp(t *testing.T, events <-chan Event, wantOp Op, timeout time.Duration) Event {
	t.Helper()

	deadline := time.After(timeout)
	for {
		select {
		case event := <-events:
			if event.Op == wantOp {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event with op %s", wantOp)
		}
	}
}
