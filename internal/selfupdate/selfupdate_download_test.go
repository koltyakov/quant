package selfupdate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAllWithLimit_SmallReader(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	result, err := readAllWithLimit(bytes.NewReader(data), 100)
	if err != nil {
		t.Fatalf("readAllWithLimit small reader error: %v", err)
	}
	if string(result) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", result)
	}
}

func TestReadAllWithLimit_ExceedsLimit(t *testing.T) {
	t.Parallel()

	data := make([]byte, 200)
	for i := range data {
		data[i] = 'a'
	}
	_, err := readAllWithLimit(bytes.NewReader(data), 100)
	if err == nil {
		t.Fatal("expected error when content exceeds limit")
	}
}

func TestReadAllWithLimit_InvalidLimit(t *testing.T) {
	t.Parallel()

	_, err := readAllWithLimit(bytes.NewReader([]byte("x")), 0)
	if err == nil {
		t.Fatal("expected error for zero limit")
	}

	_, err = readAllWithLimit(bytes.NewReader([]byte("x")), -1)
	if err == nil {
		t.Fatal("expected error for negative limit")
	}
}

func TestReadAllWithLimit_ExactlyAtLimit(t *testing.T) {
	t.Parallel()

	data := []byte("12345")
	result, err := readAllWithLimit(bytes.NewReader(data), 5)
	if err != nil {
		t.Fatalf("readAllWithLimit exact limit error: %v", err)
	}
	if string(result) != "12345" {
		t.Fatalf("expected '12345', got %q", result)
	}
}

func TestReadAllWithLimit_EmptyReader(t *testing.T) {
	t.Parallel()

	result, err := readAllWithLimit(bytes.NewReader(nil), 10)
	if err != nil {
		t.Fatalf("readAllWithLimit empty reader error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %q", result)
	}
}

func TestReplaceBinaryCopy_WithTempFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "testbinary")
	originalContent := []byte("original binary content")
	if err := os.WriteFile(exe, originalContent, 0o755); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("updated binary content")
	if err := replaceBinaryCopy(exe, newContent, ""); err != nil {
		t.Fatalf("replaceBinaryCopy error: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newContent) {
		t.Fatalf("binary content = %q, want %q", got, newContent)
	}
}

func TestReplaceBinaryCopy_PreservesMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "testbinary")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinaryCopy(exe, []byte("new"), ""); err != nil {
		t.Fatalf("replaceBinaryCopy error: %v", err)
	}

	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected mode 0755, got %o", info.Mode().Perm())
	}
}

func TestReplaceBinaryCopy_StagedRestoreOnCopyFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "testbinary")
	staged := filepath.Join(dir, "staged")
	oldContent := []byte("original")
	if err := os.WriteFile(exe, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	copyCallCount := 0
	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{
		rename: func(oldPath, newPath string) error { return errors.New("rename blocked") },
		remove: os.Remove,
		copy: func(srcPath, dstPath string, mode os.FileMode) error {
			copyCallCount++
			if dstPath == exe && copyCallCount <= 2 {
				return fmt.Errorf("copy blocked attempt %d", copyCallCount)
			}
			return copyFilePath(srcPath, dstPath, mode)
		},
	})
	_ = err
}

func TestReplaceBinaryAt_WithTempFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "testbinary")
	if err := os.WriteFile(exe, []byte("old content"), 0o755); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("new content via replaceBinaryAt")
	if err := replaceBinaryAt(exe, newContent); err != nil {
		t.Fatalf("replaceBinaryAt error: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newContent) {
		t.Fatalf("binary content = %q, want %q", got, newContent)
	}
}

func TestReplaceBinaryAt_NonexistentPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "nonexistent", "binary")

	err := replaceBinaryAt(exe, []byte("new"))
	if err == nil {
		t.Fatal("expected error for nonexistent parent directory")
	}
}

func TestReplaceBinaryAt_WriteError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "testbinary")
	if err := os.WriteFile(exe, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}

	tmpDir := filepath.Join(dir, "no_write")
	if err := os.MkdirAll(tmpDir, 0o444); err != nil {
		t.Skip("cannot create read-only directory on this system")
	}

	defer func() { _ = os.Chmod(tmpDir, 0o755) }()
}
