package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScan_BasicFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main"), 0644)

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
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("hidden"), 0644)

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
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("git config"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("hello"), 0644)

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
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\nbuild/\n"), 0644)
	os.WriteFile(filepath.Join(dir, "code.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log"), 0644)
	os.MkdirAll(filepath.Join(dir, "build"), 0755)
	os.WriteFile(filepath.Join(dir, "build", "output"), []byte("binary"), 0644)

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
	os.WriteFile(path, []byte("hello"), 0644)

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
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)

	// Sub-directory with its own gitignore ignoring *.tmp.
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "subdir", ".gitignore"), []byte("*.tmp\n"), 0644)
	os.WriteFile(filepath.Join(dir, "subdir", "keep.txt"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(dir, "subdir", "skip.tmp"), []byte("skip"), 0644)
	os.WriteFile(filepath.Join(dir, "subdir", "skip.log"), []byte("log"), 0644)

	// Root file.
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("root"), 0644)
	// .tmp at root should NOT be ignored (only subdir's gitignore has *.tmp).
	os.WriteFile(filepath.Join(dir, "root.tmp"), []byte("tmp"), 0644)

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
