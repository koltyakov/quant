package scan

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestScan_BasicFiles(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "hello")
	mustWriteFile(t, filepath.Join(dir, "b.go"), "package main")

	results, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestScan_SkipsHidden(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "visible.txt"), "hello")
	mustWriteFile(t, filepath.Join(dir, ".hidden"), "hidden")

	results, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestScan_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, ".git", "objects"))
	mustWriteFile(t, filepath.Join(dir, ".git", "config"), "git config")
	mustWriteFile(t, filepath.Join(dir, "visible.txt"), "hello")

	results, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestScan_GitIgnore(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	mustWriteFile(t, filepath.Join(dir, "code.go"), "package main")
	mustWriteFile(t, filepath.Join(dir, "debug.log"), "log")
	mustMkdirAll(t, filepath.Join(dir, "build"))
	mustWriteFile(t, filepath.Join(dir, "build", "output"), "binary")

	gi, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results, err := Scan(dir, gi)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths := make(map[string]bool)
	for _, r := range results {
		paths[filepath.Base(r.Path)] = true
	}

	if !paths["code.go"] {
		t.Error("expected code.go to be included")
	}
	if paths["debug.log"] {
		t.Error("expected debug.log to be excluded")
	}
}

func TestFileHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	mustWriteFile(t, path, "hello")

	hash, err := FileHash(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}

	hash2, _ := FileHash(path)
	if hash != hash2 {
		t.Error("expected same hash for same file")
	}
}

func TestLoadGitIgnore_NoFile(t *testing.T) {
	dir := t.TempDir()
	gi, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gi != nil {
		t.Error("expected nil when no .gitignore exists")
	}
}

func TestScan_NestedGitIgnore(t *testing.T) {
	dir := t.TempDir()

	// Root gitignore ignores *.log.
	mustWriteFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")

	// Sub-directory with its own gitignore ignoring *.tmp.
	mustMkdirAll(t, filepath.Join(dir, "subdir"))
	mustWriteFile(t, filepath.Join(dir, "subdir", ".gitignore"), "*.tmp\n")
	mustWriteFile(t, filepath.Join(dir, "subdir", "keep.txt"), "keep")
	mustWriteFile(t, filepath.Join(dir, "subdir", "skip.tmp"), "skip")
	mustWriteFile(t, filepath.Join(dir, "subdir", "skip.log"), "log")

	// Root file.
	mustWriteFile(t, filepath.Join(dir, "root.txt"), "root")
	// .tmp at root should NOT be ignored (only subdir's gitignore has *.tmp).
	mustWriteFile(t, filepath.Join(dir, "root.tmp"), "tmp")

	gi, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results, err := Scan(dir, gi)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths := make(map[string]bool)
	for _, r := range results {
		rel, _ := filepath.Rel(dir, r.Path)
		paths[rel] = true
	}

	if !paths["root.txt"] {
		t.Error("expected root.txt to be included")
	}
	if !paths["root.tmp"] {
		t.Error("expected root.tmp to be included (not covered by nested gitignore)")
	}
	if !paths[filepath.Join("subdir", "keep.txt")] {
		t.Error("expected subdir/keep.txt to be included")
	}
	if paths[filepath.Join("subdir", "skip.tmp")] {
		t.Error("expected subdir/skip.tmp to be excluded by nested gitignore")
	}
	if paths[filepath.Join("subdir", "skip.log")] {
		t.Error("expected subdir/skip.log to be excluded by root gitignore")
	}
}

func TestWalk_MatchesScanResults(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "hello")
	mustWriteFile(t, filepath.Join(dir, "b.log"), "skip")
	mustMkdirAll(t, filepath.Join(dir, "sub"))
	mustWriteFile(t, filepath.Join(dir, "sub", "c.go"), "package main")

	gi, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	results, err := Scan(dir, gi)
	if err != nil {
		t.Fatalf("unexpected scan error: %v", err)
	}

	var walked []Result
	if err := Walk(dir, gi, func(result Result) error {
		walked = append(walked, result)
		return nil
	}); err != nil {
		t.Fatalf("unexpected walk error: %v", err)
	}

	if len(walked) != len(results) {
		t.Fatalf("expected %d walked results, got %d", len(results), len(walked))
	}
	for i := range results {
		if results[i].Path != walked[i].Path {
			t.Fatalf("result %d: expected %s, got %s", i, results[i].Path, walked[i].Path)
		}
	}
}

func TestWalk_MissingRootReturnsError(t *testing.T) {
	dir := t.TempDir()
	err := Walk(filepath.Join(dir, "missing"), nil, func(Result) error { return nil })
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestWalk_VisitorErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "hello")

	want := errors.New("stop")
	err := Walk(dir, nil, func(Result) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("expected visitor error %v, got %v", want, err)
	}
}

func TestWalk_SkipsUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission semantics differ on windows")
	}

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "visible.txt"), "hello")

	locked := filepath.Join(dir, "locked")
	mustMkdirAll(t, locked)
	mustWriteFile(t, filepath.Join(locked, "secret.txt"), "secret")

	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("unexpected chmod error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0755)
	})

	results, err := Scan(dir, nil)
	if err != nil {
		t.Fatalf("expected scan to skip unreadable directory, got %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected only readable files to be returned, got %d", len(results))
	}
	if filepath.Base(results[0].Path) != "visible.txt" {
		t.Fatalf("expected visible.txt, got %s", results[0].Path)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error for %s: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("unexpected mkdir error for %s: %v", path, err)
	}
}
