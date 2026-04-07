package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrew/quant/internal/scan"
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
