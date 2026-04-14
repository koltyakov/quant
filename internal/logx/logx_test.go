package logx

import (
	"bytes"
	"log/slog"
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
	Configure("/tmp", &buf, ioDiscard{})

	Info("indexed document", "path", "/tmp/data/books/test.pdf")

	output := buf.String()
	if !strings.Contains(output, "INF") {
		t.Errorf("expected level indicator in output, got: %s", output)
	}
	if !strings.Contains(output, "data/books/test.pdf") {
		t.Errorf("expected relative path in output, got: %s", output)
	}
	if strings.Contains(output, "/tmp/data/books/test.pdf") {
		t.Errorf("expected absolute path to be relativized, got: %s", output)
	}
}

func TestPathRelativizationOutsideBase(t *testing.T) {
	var buf bytes.Buffer
	Configure("/tmp", &buf, ioDiscard{})

	Info("external path", "path", "/var/log/system.log")

	output := buf.String()
	if !strings.Contains(output, "/var/log/system.log") {
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
	tests := []struct {
		path string
		base string
		want string
	}{
		{"/tmp/data/file.txt", "/tmp", "data/file.txt"},
		{"/var/log.txt", "/tmp", "/var/log.txt"},
		{"relative/path.txt", "/tmp", "relative/path.txt"},
		{"/tmp", "/tmp", "."},
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
