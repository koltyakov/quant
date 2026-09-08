package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/lock"
)

func TestRetryQuarantined(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(root, "custom.db")
	s, err := index.NewStore(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"a.txt", "sub/b.txt"} {
		if err := s.QuarantineDocument(ctx, path, "failed", index.QuarantineRevision{Revision: "old", Category: "extract", Config: "cfg"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		s, err := index.NewStore(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		entries, err := s.ListQuarantined(ctx)
		if err != nil || len(entries) != want {
			t.Fatalf("quarantine entries = %v, err = %v; want %d", entries, err, want)
		}
	}
	lk, err := lock.TryAcquireForDB(cfg.DBPath, "primary", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := RetryQuarantined(ctx, cfg, ""); !errors.Is(err, lock.ErrLockHeld) || !strings.Contains(err.Error(), "stop all") {
		t.Fatalf("live primary error = %v", err)
	}
	_ = lk.Release()
	check(2)
	for _, path := range []string{"../outside.txt", root, "missing.txt"} {
		if err := RetryQuarantined(ctx, cfg, path); err == nil {
			t.Fatalf("expected error for %q", path)
		}
	}
	check(2)
	if err := RetryQuarantined(ctx, cfg, filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := RetryQuarantined(ctx, cfg, "sub/./b.txt"); err != nil {
		t.Fatal(err)
	}
	check(0)
	s, err = index.NewStore(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToQuarantine(ctx, "again.txt", "failed"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := RetryQuarantined(ctx, cfg, ""); err != nil {
		t.Fatal(err)
	}
	check(0)
	if err := RetryQuarantined(ctx, cfg, ""); err != nil {
		t.Fatalf("empty quarantine should succeed: %v", err)
	}
}

func TestRetryQuarantinedMissingDatabase(t *testing.T) {
	cfg := config.Default()
	cfg.WatchDir = t.TempDir()
	cfg.DBPath = filepath.Join(cfg.WatchDir, "missing.db")
	if err := RetryQuarantined(context.Background(), cfg, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(cfg.DBPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry created database: %v", err)
	}
}
