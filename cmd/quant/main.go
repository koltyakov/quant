package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/koltyakov/quant/internal/app"
	"github.com/koltyakov/quant/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func runMCP(cfg *config.Config) error {
	logFile, err := configureLogging(cfg.DBPath, cfg.WatchDir)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return app.RunMCP(ctx, cfg, Version, app.AutoUpdateHooks{
		Enabled:      isAutoUpdateEnabled,
		CheckOnStart: autoUpdateOnStart,
		StartLoop:    startAutoUpdateLoop,
	})
}
