package scan

import (
	"os"
	"path/filepath"
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
