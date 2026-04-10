package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.3.0", "0.4.0", true},
		{"0.4.0", "0.4.0", false},
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "1.3.0", true},
		{"1.2.3", "2.0.0", true},
		{"1.2.3", "1.2.3", false},
	}
	for _, tt := range tests {
		t.Run(tt.current+"->"+tt.latest, func(t *testing.T) {
			if got := isNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("isNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input string
		want  []int
	}{
		{"0.4.0", []int{0, 4, 0}},
		{"1.2.3", []int{1, 2, 3}},
		{"1.0.0-rc1", []int{1, 0, 0}},
		{"1.0.0+build7", []int{1, 0, 0}},
		{"bad", nil},
		{"1.2", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseSemver(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("parseSemver(%q) = %v, want nil", tt.input, got)
				}
				return
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("parseSemver(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAssetNameForPlatform(t *testing.T) {
	name, err := assetNameForPlatform()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "quant_") {
		t.Fatalf("assetNameForPlatform() = %q, want quant_*", name)
	}
}

func TestExtractFromTarGz(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("#!/bin/sh\necho hello\n")
	if err := tw.WriteHeader(&tar.Header{Name: "quant", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gw.Close()

	got, err := extractFromTarGz(buf.Bytes(), "quant")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("extracted content mismatch")
	}
}

func TestReplaceBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("new-binary-content")
	if err := replaceBinaryAt(exe, newContent); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newContent) {
		t.Fatalf("binary content = %q, want %q", got, newContent)
	}
}

func TestReplaceBinaryCopy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("updated-via-copy")
	if err := replaceBinaryCopy(exe, newContent, ""); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newContent) {
		t.Fatalf("binary content = %q, want %q", got, newContent)
	}
}

func TestReplaceBinaryCopyStaged_RestoresOriginalOnCreateFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	staged := filepath.Join(dir, "staged")
	oldContent := []byte("old")
	if err := os.WriteFile(exe, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	failedCreate := false
	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{
		rename: func(oldPath, newPath string) error {
			return errors.New("rename blocked")
		},
		remove: os.Remove,
		copy: func(srcPath, dstPath string, mode os.FileMode) error {
			if dstPath == exe && !failedCreate {
				failedCreate = true
				return errors.New("copy failed")
			}
			return copyFilePath(srcPath, dstPath, mode)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "create new binary") {
		t.Fatalf("expected create failure, got %v", err)
	}

	got, readErr := os.ReadFile(exe)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, oldContent) {
		t.Fatalf("binary content = %q, want %q", got, oldContent)
	}
}

func TestReplaceBinaryCopyStaged_RestoresOriginalAfterPartialWriteFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	staged := filepath.Join(dir, "staged")
	oldContent := []byte("old")
	if err := os.WriteFile(exe, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	failedCreate := false
	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{
		rename: func(oldPath, newPath string) error {
			return errors.New("rename blocked")
		},
		remove: os.Remove,
		copy: func(srcPath, dstPath string, mode os.FileMode) error {
			if dstPath == exe && !failedCreate {
				failedCreate = true
				if writeErr := os.WriteFile(dstPath, []byte("partial"), mode); writeErr != nil {
					return writeErr
				}
				return errors.New("copy failed after partial write")
			}
			return copyFilePath(srcPath, dstPath, mode)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "create new binary") {
		t.Fatalf("expected create failure, got %v", err)
	}

	got, readErr := os.ReadFile(exe)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, oldContent) {
		t.Fatalf("binary content = %q, want %q", got, oldContent)
	}
}

func TestShouldFallbackToCopy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "permission", err: syscall.EACCES, want: true},
		{name: "text file busy string", err: errors.New("rename: text file busy"), want: true},
		{name: "cross device string", err: errors.New("rename: invalid cross-device link"), want: true},
		{name: "other error", err: errors.New("rename failed"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFallbackToCopy(tt.err); got != tt.want {
				t.Fatalf("shouldFallbackToCopy() = %v, want %v", got, tt.want)
			}
		})
	}
}
