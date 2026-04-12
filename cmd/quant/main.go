package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/ingest"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/mcp"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func runMCP(cfg *config.Config) error {
	logFile, err := configureLogging(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("error configuring logging: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if isAutoUpdateEnabled() {
		if autoUpdateOnStart(ctx, Version) {
			return errRestartRequired
		}
	}

	embedder, err := embed.NewOllama(ctx, cfg.EmbedURL, cfg.EmbedModel)
	if err != nil {
		return fmt.Errorf("error connecting to ollama: %w", err)
	}
	defer func() {
		if err := embedder.Close(); err != nil {
			logx.Error("closing embedder failed", "err", err)
		}
	}()

	logx.Info("connected to embedding backend", "provider", "ollama", "model", cfg.EmbedModel, "dimensions", embedder.Dimensions())

	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("error opening database: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logx.Error("closing store failed", "err", err)
		}
	}()

	logx.Info("database opened", "path", cfg.DBPath)
	store.SetMaxVectorSearchCandidates(cfg.MaxVectorCandidates)
	if cfg.KeywordWeight > 0 || cfg.VectorWeight > 0 {
		store.SetWeightOverrides(float32(cfg.KeywordWeight), float32(cfg.VectorWeight))
	}

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{
		Model:      cfg.EmbedModel,
		Dimensions: embedder.Dimensions(),
		Normalized: true,
	})
	if err != nil {
		return fmt.Errorf("error configuring embedding metadata: %w", err)
	}
	if rebuild {
		logx.Info("embedding metadata changed; rebuilding index from filesystem projection")
	}

	gi, err := scan.LoadGitIgnore(cfg.WatchDir)
	if err != nil {
		return fmt.Errorf("error loading gitignore: %w", err)
	}

	idx := &indexer{
		cfg:       cfg,
		store:     store,
		hnswStore: store,
		embedder:  embedder,
		extractor: extract.NewRouter(extract.Options{PDFOCRLang: cfg.PDFOCRLang, PDFOCRTimeout: cfg.PDFOCRTimeout}),
		pipeline: &ingest.Pipeline{
			Embedder:  embedder,
			ChunkSize: cfg.ChunkSize,
			Overlap:   cfg.ChunkOverlap,
			BatchSize: cfg.EmbedBatchSize,
		},
		paths:   newPathSyncTracker(),
		live:    newLiveIndexQueue(liveQueueSizeForWorkers(cfg.IndexWorkers)),
		retries: newRetryScheduler(),
	}

	watcher, err := watch.New(cfg.WatchDir, gi, watch.Options{EventBuffer: cfg.WatchEventBuffer})
	if err != nil {
		return fmt.Errorf("error starting watcher: %w", err)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			logx.Error("closing watcher failed", "err", err)
		}
	}()

	var wg sync.WaitGroup
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	var needsRestart atomic.Bool
	if isAutoUpdateEnabled() {
		go startAutoUpdateLoop(serverCtx, Version, func() {
			needsRestart.Store(true)
			serverCancel()
		})
	}

	idx.startLiveIndexWorkers(serverCtx, &wg)

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.watchLoop(serverCtx, watcher)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.runInitialSync(serverCtx)
	}()

	mcpServer := mcp.NewServer(cfg, store, embedder, Version)
	logx.Info("starting MCP server", "transport", cfg.Transport)

	if err := mcpServer.Serve(serverCtx, cfg); err != nil {
		serverCancel()
		wg.Wait()
		if needsRestart.Load() {
			return errRestartRequired
		}
		return err
	}

	serverCancel()
	wg.Wait()
	if needsRestart.Load() {
		return errRestartRequired
	}
	return nil
}
