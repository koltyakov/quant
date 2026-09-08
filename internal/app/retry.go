package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/lock"
)

// RetryQuarantined clears quarantine under exclusive database ownership.
// The next MCP startup performs the rescan; this operation does not index files.
func RetryQuarantined(ctx context.Context, cfg *config.Config, path string) (err error) {
	if cfg.NoLock || cfg.ProxyAddr != "" {
		return fmt.Errorf("quarantine retry requires local database locking; stop all MCP instances and retry without no-lock or proxy settings")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := ""
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.WatchDir, path)
		}
		key, err = DocumentKey(cfg.WatchDir, path)
		if err != nil {
			return fmt.Errorf("invalid retry path: %w", err)
		}
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return fmt.Errorf("cannot retry existing database %s: %w", cfg.DBPath, err)
	}
	lk, err := lock.TryAcquireForDB(cfg.DBPath, generateInstanceID(), "")
	if errors.Is(err, lock.ErrLockHeld) {
		return fmt.Errorf("cannot retry quarantine while database %s is in use; stop all quant MCP instances for this database, including workers, then rerun quant retry and start MCP again: %w", cfg.DBPath, err)
	}
	if err != nil {
		return fmt.Errorf("acquiring retry lock: %w", err)
	}
	defer func() { err = errors.Join(err, lk.Release()) }()
	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening retry database: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if key == "" {
		return store.ClearQuarantine(ctx)
	}
	found, err := store.IsQuarantined(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("file %q is not quarantined in %s", key, cfg.DBPath)
	}
	return store.RemoveFromQuarantine(ctx, key)
}
