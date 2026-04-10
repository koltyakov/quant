package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	logRotateMaxSize    int64 = 10 * 1024 * 1024
	logRotateMaxBackups       = 5
)

type rotatingLogWriter struct {
	path       string
	maxSize    int64
	maxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

func logPathForDB(dbPath string) string {
	ext := filepath.Ext(dbPath)
	if ext == "" {
		return dbPath + ".log"
	}
	return strings.TrimSuffix(dbPath, ext) + ".log"
}

func configureLogging(dbPath string) (io.WriteCloser, error) {
	logPath := logPathForDB(dbPath)
	logWriter, err := newRotatingLogWriter(logPath, logRotateMaxSize, logRotateMaxBackups)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, logWriter))
	return logWriter, nil
}

func newRotatingLogWriter(path string, maxSize int64, maxBackups int) (*rotatingLogWriter, error) {
	w := &rotatingLogWriter{
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", path, err)
	}
	return w, nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rotateIfNeeded(int64(len(p))); err != nil {
		return 0, err
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.size = 0
	return err
}

func (w *rotatingLogWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0755); err != nil {
		return err
	}

	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}

	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingLogWriter) rotateIfNeeded(incoming int64) error {
	if w.file == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	if w.maxSize <= 0 || w.size == 0 || w.size+incoming <= w.maxSize {
		return nil
	}
	return w.rotate()
}

func (w *rotatingLogWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	if w.maxBackups > 0 {
		oldest := rotatedLogPath(w.path, w.maxBackups)
		_ = os.Remove(oldest)
		for i := w.maxBackups - 1; i >= 1; i-- {
			src := rotatedLogPath(w.path, i)
			dst := rotatedLogPath(w.path, i+1)
			if _, err := os.Stat(src); err == nil {
				_ = os.Remove(dst)
				if err := os.Rename(src, dst); err != nil {
					return err
				}
			}
		}
		if _, err := os.Stat(w.path); err == nil {
			dst := rotatedLogPath(w.path, 1)
			_ = os.Remove(dst)
			if err := os.Rename(w.path, dst); err != nil {
				return err
			}
		}
	} else {
		_ = os.Remove(w.path)
	}

	w.size = 0
	return w.open()
}

func rotatedLogPath(path string, generation int) string {
	return fmt.Sprintf("%s.%d", path, generation)
}

func isCompanionLogPathForDB(dbPath, path string) bool {
	base := filepath.Clean(logPathForDB(dbPath))
	path = filepath.Clean(path)
	if path == base {
		return true
	}
	if !strings.HasPrefix(path, base+".") {
		return false
	}
	suffix := strings.TrimPrefix(path, base+".")
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
