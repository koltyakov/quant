package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureNotOLE2_OLE2File(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "ole2.doc")
	content := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("rest of file")...)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	err := ensureNotOLE2(path)
	if !errors.Is(err, ErrEncrypted) {
		t.Fatalf("expected ErrEncrypted, got %v", err)
	}
}

func TestEnsureNotOLE2_NormalFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "normal.docx")
	if err := os.WriteFile(path, []byte("PK\x03\x04normal zip content"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	if err := ensureNotOLE2(path); err != nil {
		t.Fatalf("expected nil for non-OLE2 file, got %v", err)
	}
}

func TestEnsureNotOLE2_NonexistentFile(t *testing.T) {
	t.Parallel()

	err := ensureNotOLE2("/nonexistent/path/file.doc")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestEnsureNotOLE2_SmallFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.txt")
	if err := os.WriteFile(path, []byte("ab"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	if err := ensureNotOLE2(path); err != nil {
		t.Fatalf("expected nil for file too small to be OLE2, got %v", err)
	}
}

func TestCheckContext_NilContext(t *testing.T) {
	t.Parallel()

	if err := checkContext(context.TODO()); err != nil {
		t.Fatalf("expected nil for nil context, got %v", err)
	}
}

func TestCheckContext_BackgroundContext(t *testing.T) {
	t.Parallel()

	if err := checkContext(context.Background()); err != nil {
		t.Fatalf("expected nil for background context, got %v", err)
	}
}

func TestCheckContext_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := checkContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadZipFile_FoundEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("entry.txt")
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("unexpected open reader error: %v", err)
	}
	defer func() { _ = zr.Close() }()

	data, err := readZipFile(context.Background(), zr.File, "entry.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected %q, got %q", "hello", string(data))
	}
}

func TestReadZipFile_EntryNotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("other.txt")
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("unexpected open reader error: %v", err)
	}
	defer func() { _ = zr.Close() }()

	_, err = readZipFile(context.Background(), zr.File, "missing.txt")
	if err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
	if !strings.Contains(err.Error(), "zip entry not found") {
		t.Fatalf("expected zip entry not found error, got %v", err)
	}
}

func TestReadZipFile_MultipleEntries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "multi.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("unexpected create error: %v", err)
		}
		if _, err := w.Write([]byte("content-" + name)); err != nil {
			t.Fatalf("unexpected write error: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("unexpected open reader error: %v", err)
	}
	defer func() { _ = zr.Close() }()

	data, err := readZipFile(context.Background(), zr.File, "b.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "content-b.txt" {
		t.Fatalf("expected %q, got %q", "content-b.txt", string(data))
	}
}

func TestReadZipFile_CanceledContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("entry.txt")
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("unexpected open reader error: %v", err)
	}
	defer func() { _ = zr.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = readZipFile(ctx, zr.File, "entry.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadAllLimited_WithinLimit(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	r := bytes.NewReader(data)
	limit := int64(len(data) * 2)

	result, err := readAllLimited(context.Background(), r, limit, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(data) {
		t.Fatalf("expected %q, got %q", string(data), string(result))
	}
}

func TestReadAllLimited_ExceedsLimit(t *testing.T) {
	t.Parallel()

	data := []byte(strings.Repeat("a", 100))
	r := bytes.NewReader(data)
	limit := int64(50)

	_, err := readAllLimited(context.Background(), r, limit, "test")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestReadAllLimited_CanceledContextBefore(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	r := bytes.NewReader(data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := readAllLimited(ctx, r, 1024, "test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func TestReadAllLimited_ReaderError(t *testing.T) {
	t.Parallel()

	r := &errorReader{err: io.ErrUnexpectedEOF}
	_, err := readAllLimited(context.Background(), r, 1024, "test")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

type slowReader struct {
	data   []byte
	offset int
	block  chan struct{}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	<-r.block
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func TestReadAllLimited_CanceledContextDuringRead(t *testing.T) {
	block := make(chan struct{})
	r := &slowReader{data: []byte("hello world"), block: block}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error)
	go func() {
		_, err := readAllLimited(ctx, r, 1024, "test")
		done <- err
	}()

	cancel()
	close(block)

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadFileLimited_NormalRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := []byte("hello file")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	data, err := readFileLimited(context.Background(), path, maxExtractorFileSize)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("expected %q, got %q", string(content), string(data))
	}
}

func TestReadFileLimited_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := readFileLimited(ctx, "/dev/null", maxExtractorFileSize)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadFileLimited_FileTooLarge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if err := f.Truncate(100); err != nil {
		_ = f.Close()
		t.Fatalf("unexpected truncate error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	_, err = readFileLimited(context.Background(), path, 50)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestReadFileLimited_NonexistentFile(t *testing.T) {
	t.Parallel()

	_, err := readFileLimited(context.Background(), "/nonexistent/path/file.txt", maxExtractorFileSize)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestNextXMLToken_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decoder := xml.NewDecoder(strings.NewReader("<root/>"))
	_, err := nextXMLToken(ctx, decoder)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestNextXMLToken_ValidToken(t *testing.T) {
	t.Parallel()

	decoder := xml.NewDecoder(strings.NewReader("<root>text</root>"))
	tok, err := nextXMLToken(context.Background(), decoder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	se, ok := tok.(xml.StartElement)
	if !ok {
		t.Fatalf("expected xml.StartElement, got %T", tok)
	}
	if se.Name.Local != "root" {
		t.Fatalf("expected element 'root', got %q", se.Name.Local)
	}
}

func TestMinInt64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b, want int64
	}{
		{1, 2, 1},
		{2, 1, 1},
		{5, 5, 5},
		{0, -1, -1},
		{-10, 10, -10},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_%d", tt.a, tt.b), func(t *testing.T) {
			got := minInt64(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("minInt64(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestReadZipFile_EntryTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("large.txt")
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	bigData := make([]byte, maxExtractorFileSize+1)
	if _, err := w.Write(bigData); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("unexpected open reader error: %v", err)
	}
	defer func() { _ = zr.Close() }()

	_, err = readZipFile(context.Background(), zr.File, "large.txt")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestFormatExtractBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
		{1125899906842624, "1.0 PB"},
		{1152921504606846976, "1.0 EB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatExtractBytes(tt.input)
			if got != tt.want {
				t.Errorf("formatExtractBytes(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
