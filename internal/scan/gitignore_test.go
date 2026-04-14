package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitIgnoreMatcherReloadAndRemove(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	mustMkdirAll(t, subdir)

	mustWriteFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")
	mustWriteFile(t, filepath.Join(subdir, ".gitignore"), "*.tmp\n")

	root, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("LoadGitIgnore() error = %v", err)
	}

	matcher := NewGitIgnoreMatcher(dir, root)

	rootLog := filepath.Join(dir, "debug.log")
	nestedTmp := filepath.Join(subdir, "cache.tmp")
	nestedLog := filepath.Join(subdir, "nested.log")

	if !matcher.Matches(rootLog) {
		t.Fatal("Matches(root log) = false, want true")
	}
	if matcher.Matches(nestedTmp) {
		t.Fatal("Matches(nested tmp) before load = true, want false")
	}

	matcher.Load(subdir)

	if !matcher.Matches(nestedTmp) {
		t.Fatal("Matches(nested tmp) after load = false, want true")
	}
	if !matcher.Matches(nestedLog) {
		t.Fatal("Matches(nested log) = false, want true")
	}

	matcher.Remove(subdir)

	if matcher.Matches(nestedTmp) {
		t.Fatal("Matches(nested tmp) after remove = true, want false")
	}
	if !matcher.Matches(nestedLog) {
		t.Fatal("Matches(nested log) after remove = false, want true")
	}
}

func TestGitIgnoreMatcherReloadRootRemovalClearsMatcher(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")

	root, err := LoadGitIgnore(dir)
	if err != nil {
		t.Fatalf("LoadGitIgnore() error = %v", err)
	}

	matcher := NewGitIgnoreMatcher(dir, root)
	logPath := filepath.Join(dir, "app.log")
	if !matcher.Matches(logPath) {
		t.Fatal("Matches(app.log) = false, want true")
	}

	if err := os.Remove(filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatalf("Remove(.gitignore) error = %v", err)
	}

	matcher.Reload(dir)

	if matcher.Matches(logPath) {
		t.Fatal("Matches(app.log) after root reload = true, want false")
	}
}

func TestGitIgnoreMatcherMatchesRootDirAndInvalidPathAsFalse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	matcher := NewGitIgnoreMatcher(dir, nil)

	if matcher.Matches(dir) {
		t.Fatal("Matches(root dir) = true, want false")
	}
	if matcher.Matches(string([]byte{0xff, 0xfe})) {
		t.Fatal("Matches(invalid path) = true, want false")
	}
}

func TestIsHiddenName(t *testing.T) {
	t.Parallel()

	if !IsHiddenName(".gitignore") {
		t.Fatal("IsHiddenName(.gitignore) = false, want true")
	}
	if IsHiddenName("visible.txt") {
		t.Fatal("IsHiddenName(visible.txt) = true, want false")
	}
}
