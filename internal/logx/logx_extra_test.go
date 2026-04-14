package logx

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetOutputReplacesGlobalLogger(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() {
		SetOutput(ioDiscard{})
	})

	Info("from set output", "k", "v")

	output := buf.String()
	if !strings.Contains(output, "msg=\"from set output\"") {
		t.Fatalf("expected text handler output, got: %s", output)
	}
	if !strings.Contains(output, "k=v") {
		t.Fatalf("expected structured attribute in output, got: %s", output)
	}
}

func TestDualHandlerWithAttrsAndGroupRelativizesPaths(t *testing.T) {
	var console bytes.Buffer
	var file bytes.Buffer

	baseDir := filepath.Join(string(filepath.Separator), "tmp")
	handler := (&dualHandler{
		console:     &console,
		fileHandler: slog.NewTextHandler(&file, &slog.HandlerOptions{}),
		baseDir:     baseDir,
	}).WithAttrs([]slog.Attr{
		slog.String("path", filepath.Join(baseDir, "project", "root.txt")),
		slog.String("kind", "base"),
	}).WithGroup("req")

	record := slog.NewRecord(time.Unix(1700000000, 0), slog.LevelInfo, "grouped", 0)
	record.AddAttrs(slog.String("path", filepath.Join(baseDir, "project", "child.txt")))
	record.AddAttrs(slog.Int("id", 42))

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	consoleOut := console.String()
	if !strings.Contains(consoleOut, "path=project/root.txt") {
		t.Fatalf("expected relativized inherited path in console output, got: %s", consoleOut)
	}
	if !strings.Contains(consoleOut, "path=project/child.txt") {
		t.Fatalf("expected relativized record path in console output, got: %s", consoleOut)
	}
	if !strings.Contains(consoleOut, "kind=base") {
		t.Fatalf("expected inherited attr in console output, got: %s", consoleOut)
	}

	fileOut := file.String()
	if !strings.Contains(fileOut, "path=project/root.txt") {
		t.Fatalf("expected inherited path in file output, got: %s", fileOut)
	}
	if !strings.Contains(fileOut, "kind=base") {
		t.Fatalf("expected inherited attr in file output, got: %s", fileOut)
	}
	if !strings.Contains(fileOut, "req.path=project/child.txt") {
		t.Fatalf("expected grouped record path in file output, got: %s", fileOut)
	}
	if !strings.Contains(fileOut, "req.id=42") {
		t.Fatalf("expected grouped record attr in file output, got: %s", fileOut)
	}
}

func TestLevelColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level slog.Level
		want  string
	}{
		{level: slog.LevelDebug, want: "\x1b[90m"},
		{level: slog.LevelInfo, want: "\x1b[36m"},
		{level: slog.LevelWarn, want: "\x1b[33m"},
		{level: slog.LevelError, want: "\x1b[31m"},
	}

	for _, tt := range tests {
		if got := levelColor(tt.level); got != tt.want {
			t.Fatalf("levelColor(%v) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestIsTerminalFalseCases(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if isTerminal(&buf) {
		t.Fatal("isTerminal(bytes.Buffer) = true, want false")
	}

	f, err := os.CreateTemp(t.TempDir(), "logx-terminal-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = f.Close()
	})

	if isTerminal(f) {
		t.Fatal("isTerminal(temp file) = true, want false")
	}
}
