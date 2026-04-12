package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/mcp"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

var ErrRestartRequired = errors.New("restart required")

type AutoUpdateHooks struct {
	Enabled      func() bool
	CheckOnStart func(ctx context.Context, currentVersion string) bool
	StartLoop    func(ctx context.Context, currentVersion string, onUpdate func())
}

func RunMCP(ctx context.Context, cfg *config.Config, version string, hooks AutoUpdateHooks) error {
	configureProcessMemory()

	if hooks.Enabled != nil && hooks.Enabled() {
		if hooks.CheckOnStart != nil && hooks.CheckOnStart(ctx, version) {
			return ErrRestartRequired
		}
	}

	rawEmbedder, err := embed.NewOllama(ctx, cfg.EmbedURL, cfg.EmbedModel)
	if err != nil {
		return fmt.Errorf("error connecting to ollama: %w", err)
	}
	defer func() {
		if err := rawEmbedder.Close(); err != nil {
			logx.Error("closing embedder failed", "err", err)
		}
	}()

	logx.Info("connected to embedding backend", "provider", "ollama", "model", cfg.EmbedModel, "dimensions", rawEmbedder.Dimensions())

	// Wrap the raw embedder with caching, flight dedup, and circuit breaker
	// for query-time Embed() calls. EmbedBatch (used during indexing) passes through.
	embedder := embed.NewCachingEmbedder(rawEmbedder, embed.CachingConfig{
		CacheSize:           128,
		CircuitFailureLimit: 5,
		CircuitResetTimeout: 30 * time.Second,
		NormalizeFunc:       index.NormalizeFloat32,
	})

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

	idx := NewIndexer(IndexerConfig{
		Cfg:       cfg,
		Store:     store,
		HNSWStore: store,
		Embedder:  rawEmbedder,
		Extractor: extract.NewRouter(extract.Options{PDFOCRLang: cfg.PDFOCRLang, PDFOCRTimeout: cfg.PDFOCRTimeout}),
	})

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
	if hooks.Enabled != nil && hooks.Enabled() && hooks.StartLoop != nil {
		go hooks.StartLoop(serverCtx, version, func() {
			needsRestart.Store(true)
			serverCancel()
		})
	}

	idx.StartLiveIndexWorkers(serverCtx, &wg)

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.WatchLoop(serverCtx, watcher)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.RunInitialSync(serverCtx)
	}()

	mcpServer := mcp.NewServer(cfg, store, embedder, version, idx.IndexState)
	logx.Info("starting MCP server", "transport", cfg.Transport)

	if err := mcpServer.Serve(serverCtx, cfg); err != nil {
		serverCancel()
		wg.Wait()
		if needsRestart.Load() {
			return ErrRestartRequired
		}
		return err
	}

	serverCancel()
	wg.Wait()
	if needsRestart.Load() {
		return ErrRestartRequired
	}
	return nil
}
