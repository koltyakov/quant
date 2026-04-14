package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var mu sync.Mutex

var logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

func Configure(baseDir string, console io.Writer, file io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	logger = slog.New(&dualHandler{
		console:     console,
		consoleTerm: isTerminal(console),
		fileHandler: slog.NewTextHandler(file, &slog.HandlerOptions{}),
		baseDir:     filepath.Clean(baseDir),
	})
}

func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{}))
}

func Debug(msg string, args ...any) {
	mu.Lock()
	l := logger
	mu.Unlock()
	l.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	mu.Lock()
	l := logger
	mu.Unlock()
	l.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	mu.Lock()
	l := logger
	mu.Unlock()
	l.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	mu.Lock()
	l := logger
	mu.Unlock()
	l.Error(msg, args...)
}

type dualHandler struct {
	console     io.Writer
	consoleTerm bool
	fileHandler slog.Handler
	baseDir     string
	attrs       []slog.Attr
}

func (h *dualHandler) Enabled(_ context.Context, level slog.Level) bool {
	return true
}

func (h *dualHandler) Handle(_ context.Context, r slog.Record) error {
	var recordAttrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		recordAttrs = append(recordAttrs, h.relativizeAttr(a))
		return true
	})

	newRecord := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	for _, a := range recordAttrs {
		newRecord.AddAttrs(a)
	}
	_ = h.fileHandler.Handle(context.Background(), newRecord)

	allAttrs := make([]slog.Attr, 0, len(h.attrs)+len(recordAttrs))
	allAttrs = append(allAttrs, h.attrs...)
	allAttrs = append(allAttrs, recordAttrs...)
	h.writeConsole(r.Time, r.Level, r.Message, allAttrs)

	return nil
}

func (h *dualHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	rel := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		rel[i] = h.relativizeAttr(a)
	}

	newAttrs := make([]slog.Attr, len(h.attrs)+len(rel))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], rel)

	return &dualHandler{
		console:     h.console,
		consoleTerm: h.consoleTerm,
		fileHandler: h.fileHandler.WithAttrs(rel),
		baseDir:     h.baseDir,
		attrs:       newAttrs,
	}
}

func (h *dualHandler) WithGroup(name string) slog.Handler {
	return &dualHandler{
		console:     h.console,
		consoleTerm: h.consoleTerm,
		fileHandler: h.fileHandler.WithGroup(name),
		baseDir:     h.baseDir,
		attrs:       h.attrs,
	}
}

func (h *dualHandler) relativizeAttr(a slog.Attr) slog.Attr {
	if a.Key == "path" {
		if s, ok := a.Value.Any().(string); ok {
			return slog.String("path", relativizePath(s, h.baseDir))
		}
	}
	return a
}

func relativizePath(path, base string) string {
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	if strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func (h *dualHandler) writeConsole(t time.Time, level slog.Level, msg string, attrs []slog.Attr) {
	ts := t.Format("15:04:05")
	lvl := levelStr(level)

	var b strings.Builder
	if h.consoleTerm {
		b.WriteString("\x1b[90m")
		b.WriteString(ts)
		b.WriteString("\x1b[0m ")
		b.WriteString(levelColor(level))
	} else {
		b.WriteString(ts)
		b.WriteByte(' ')
	}
	b.WriteString(lvl)
	if h.consoleTerm {
		b.WriteString("\x1b[0m")
	}
	b.WriteByte(' ')
	b.WriteString(msg)

	for _, a := range attrs {
		b.WriteString("  ")
		b.WriteString(a.Key)
		b.WriteByte('=')
		fmt.Fprintf(&b, "%v", a.Value)
	}
	b.WriteByte('\n')

	_, _ = h.console.Write([]byte(b.String()))
}

func levelStr(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERR"
	case level >= slog.LevelWarn:
		return "WRN"
	case level >= slog.LevelInfo:
		return "INF"
	default:
		return "DBG"
	}
}

func levelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "\x1b[31m"
	case level >= slog.LevelWarn:
		return "\x1b[33m"
	case level >= slog.LevelInfo:
		return "\x1b[36m"
	default:
		return "\x1b[90m"
	}
}

func isTerminal(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return fi.Mode()&os.ModeCharDevice != 0
	}
	return false
}
