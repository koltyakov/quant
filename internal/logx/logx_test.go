package logx

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFunctionsDoNotPanic(t *testing.T) {
	var buf bytes.Buffer
	Configure("/tmp", &buf, &buf)

	Debug("debug msg")
	Info("info msg")
	Warn("warn msg")
	Error("error msg")

	console := buf.String()
	for _, msg := range []string{"info msg", "warn msg", "error msg"} {
		if !strings.Contains(console, msg) {
			t.Errorf("expected %q in console output", msg)
		}
	}
}

func TestConsoleFormat(t *testing.T) {
	var buf bytes.Buffer
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "data", "books", "test.pdf")
	Configure(baseDir, &buf, ioDiscard{})

	Info("indexed document", "path", path)

	output := buf.String()
	if !strings.Contains(output, "INF") {
		t.Errorf("expected level indicator in output, got: %s", output)
	}
	if !strings.Contains(output, "data/books/test.pdf") {
		t.Errorf("expected relative path in output, got: %s", output)
	}
	if strings.Contains(output, filepath.ToSlash(path)) {
		t.Errorf("expected absolute path to be relativized, got: %s", output)
	}
}

func TestPathRelativizationOutsideBase(t *testing.T) {
	var buf bytes.Buffer
	baseDir := t.TempDir()
	externalPath := filepath.Join(t.TempDir(), "system.log")
	Configure(baseDir, &buf, ioDiscard{})

	Info("external path", "path", externalPath)

	output := buf.String()
	if !strings.Contains(output, filepath.ToSlash(externalPath)) {
		t.Errorf("expected absolute path preserved when outside base dir, got: %s", output)
	}
}

func TestDualHandlerWritesToBothOutputs(t *testing.T) {
	var console bytes.Buffer
	var file bytes.Buffer
	Configure("/tmp", &console, &file)

	Info("test message", "key", "value")

	c := console.String()
	f := file.String()

	if !strings.Contains(c, "test message") {
		t.Errorf("console missing message, got: %s", c)
	}
	if !strings.Contains(f, "test message") {
		t.Errorf("file missing message, got: %s", f)
	}
	if !strings.Contains(f, "level=INFO") {
		t.Errorf("file missing structured level, got: %s", f)
	}
	if !strings.Contains(c, "INF") {
		t.Errorf("console missing level indicator, got: %s", c)
	}
}

func TestRelativizePath(t *testing.T) {
	baseDir := t.TempDir()
	externalPath := filepath.Join(t.TempDir(), "log.txt")
	tests := []struct {
		path string
		base string
		want string
	}{
		{filepath.Join(baseDir, "data", "file.txt"), baseDir, "data/file.txt"},
		{externalPath, baseDir, filepath.ToSlash(externalPath)},
		{filepath.Join("relative", "path.txt"), baseDir, "relative/path.txt"},
		{baseDir, baseDir, "."},
	}
	for _, tt := range tests {
		got := relativizePath(tt.path, tt.base)
		if got != tt.want {
			t.Errorf("relativizePath(%q, %q) = %q, want %q", tt.path, tt.base, got, tt.want)
		}
	}
}

func TestLevelStr(t *testing.T) {
	tests := []struct {
		level int
		want  string
	}{
		{-4, "DBG"},
		{0, "INF"},
		{4, "WRN"},
		{8, "ERR"},
	}
	for _, tt := range tests {
		got := levelStr(slog.Level(tt.level))
		if got != tt.want {
			t.Errorf("levelStr(%d) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
