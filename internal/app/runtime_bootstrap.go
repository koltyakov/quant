package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/llm"
	"github.com/koltyakov/quant/internal/lock"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/mcp"
	"github.com/koltyakov/quant/internal/proxy"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

type mcpProcessServer interface {
	Serve(ctx context.Context, cfg *config.Config) error
}

type processRunner struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	restart atomic.Bool
}

func newProcessRunner(parent context.Context) *processRunner {
	ctx, cancel := context.WithCancel(parent)
	return &processRunner{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (r *processRunner) Go(fn func()) {
	r.wg.Go(fn)
}

func (r *processRunner) requestRestart() {
	r.restart.Store(true)
	r.cancel()
}

func (r *processRunner) Run(server mcpProcessServer, cfg *config.Config) error {
	err := server.Serve(r.ctx, cfg)
	r.cancel()
	r.wg.Wait()
	if r.restart.Load() {
		return ErrRestartRequired
	}
	return err
}

type mainProcess struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       *config.Config
	version   string
	lock      *lock.Lock
	closeOnce sync.Once

	rawEmbedder    embed.Embedder
	searchEmbedder embed.Embedder
	store          *index.Store
	idx            *Indexer
	watcher        *watch.Watcher
	proxyServer    *proxy.Server
	proxyAddr      string
}

func newMainProcess(ctx context.Context, cfg *config.Config, version string, lk *lock.Lock) (_ *mainProcess, err error) {
	serviceCtx, cancel := context.WithCancel(ctx)
	proc := &mainProcess{
		ctx:     serviceCtx,
		cancel:  cancel,
		cfg:     cfg,
		version: version,
		lock:    lk,
	}
	defer func() {
		if err != nil {
			cancel()
			proc.Close()
		}
	}()

	proc.rawEmbedder, proc.searchEmbedder, err = newRuntimeEmbedders(serviceCtx, cfg)
	if err != nil {
		return nil, err
	}

	featureCompleter, err := newRuntimeCompleter(cfg)
	if err != nil {
		return nil, err
	}

	proc.store, err = openMainStore(serviceCtx, cfg, proc.rawEmbedder, featureCompleter)
	if err != nil {
		return nil, err
	}

	proc.idx = newMainIndexer(cfg, proc.store, proc.rawEmbedder, featureCompleter)

	proc.proxyServer = proxy.NewServer(proc.store, proc.idx.IndexState, proc.searchEmbedder)
	proc.proxyAddr, err = proc.proxyServer.Start(serviceCtx)
	if err != nil {
		return nil, fmt.Errorf("starting proxy server: %w", err)
	}

	if lk != nil {
		lk.UpdateProxyAddr(proc.proxyAddr)
	}

	proc.watcher, err = newMainWatcher(cfg)
	if err != nil {
		return nil, err
	}

	return proc, nil
}

func (p *mainProcess) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		if p.proxyServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := p.proxyServer.Shutdown(shutdownCtx); err != nil {
				logx.Error("shutting down proxy server failed", "err", err)
			}
			cancel()
		}
		if p.watcher != nil {
			if err := p.watcher.Close(); err != nil {
				logx.Error("closing watcher failed", "err", err)
			}
		}
		if p.rawEmbedder != nil {
			if err := p.rawEmbedder.Close(); err != nil {
				logx.Error("closing embedder failed", "err", err)
			}
		}
		if p.store != nil {
			if err := p.store.Close(); err != nil {
				logx.Error("closing store failed", "err", err)
			}
		}
		if p.lock != nil {
			if err := p.lock.Release(); err != nil {
				logx.Error("releasing lock failed", "err", err)
			}
		}
	})
}

func (p *mainProcess) Serve(hooks AutoUpdateHooks) error {
	runner := newProcessRunner(p.ctx)

	if hooks.Enabled != nil && hooks.Enabled() && hooks.StartLoop != nil {
		go hooks.StartLoop(runner.ctx, p.version, runner.requestRestart)
	}

	p.idx.StartLiveIndexWorkers(runner.ctx, &runner.wg)
	runner.Go(func() { p.idx.WatchLoop(runner.ctx, p.watcher) })
	runner.Go(func() { p.idx.RunInitialSync(runner.ctx) })

	if p.cfg.HNSWReoptimizeThreshold > 0 {
		runner.Go(func() { p.idx.RunHNSWReoptimizer(runner.ctx, p.store, p.cfg.HNSWReoptimizeThreshold) })
	}

	runner.Go(func() { p.idx.RunHNSWPeriodicFlush(runner.ctx, p.store) })
	runner.Go(func() { p.idx.RunPeriodicVacuum(runner.ctx, p.store) })

	mcpServer := mcp.NewServer(p.cfg, p.store, p.searchEmbedder, p.version, p.idx.IndexState)
	logx.Info("starting MCP server (main)", "transport", p.cfg.Transport, "proxy_addr", p.proxyAddr)
	return runner.Run(mcpServer, p.cfg)
}

func newRuntimeEmbedders(ctx context.Context, cfg *config.Config) (embed.Embedder, embed.Embedder, error) {
	rawEmbedder, err := embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel, cfg.EmbedAPIKey)
	if err != nil && isLocalURL(cfg.EmbedURL) {
		if ollamaPath, lookErr := exec.LookPath("ollama"); lookErr == nil {
			if errors.Is(err, embed.ErrOllamaUnavailable) {
				logx.Info("Ollama not running; attempting to start it automatically", "binary", ollamaPath)
				if startErr := autoStartOllama(ctx); startErr != nil {
					logx.Warn("auto-start Ollama failed", "err", startErr)
				} else {
					rawEmbedder, err = embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel, cfg.EmbedAPIKey)
				}
			}
			if errors.Is(err, embed.ErrModelNotFound) {
				logx.Info("embedding model not found; pulling from Ollama (this may take a while)", "model", cfg.EmbedModel)
				if pullErr := pullOllamaModel(ctx, cfg.EmbedModel); pullErr != nil {
					logx.Warn("auto-pull model failed", "model", cfg.EmbedModel, "err", pullErr)
				} else {
					rawEmbedder, err = embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel, cfg.EmbedAPIKey)
				}
			}
		}
	}
	if err != nil {
		if !errors.Is(err, embed.ErrOllamaUnavailable) && !errors.Is(err, embed.ErrModelNotFound) {
			return nil, nil, fmt.Errorf("error connecting to embedding backend: %w", err)
		}
		logx.Warn("embedding backend unavailable; MCP will start in keyword-only mode — run 'ollama serve' to enable vector search", "err", err)
		return nil, nil, nil
	}

	logx.Info("connected to embedding backend", "provider", cfg.EmbedProvider, "model", cfg.EmbedModel, "dimensions", rawEmbedder.Dimensions())
	searchEmbedder := embed.NewCachingEmbedder(rawEmbedder, embed.CachingConfig{
		CacheSize:           128,
		CircuitFailureLimit: 5,
		CircuitResetTimeout: 30 * time.Second,
		NormalizeFunc:       index.NormalizeFloat32,
		ModelID:             cfg.EmbedModel,
	})

	return rawEmbedder, searchEmbedder, nil
}

func newRuntimeCompleter(cfg *config.Config) (llm.Completer, error) {
	if cfg.RerankerType == "" && !cfg.SummarizerEnabled {
		return nil, nil
	}

	completer, err := llm.NewCompleter(llm.Config{
		Provider: llm.ProviderType(cfg.LLMProvider),
		BaseURL:  cfg.LLMURL,
		APIKey:   cfg.LLMAPIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("error configuring llm backend: %w", err)
	}
	return completer, nil
}

func openMainStore(ctx context.Context, cfg *config.Config, rawEmbedder embed.Embedder, featureCompleter llm.Completer) (*index.Store, error) {
	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}

	logx.Info("database opened", "path", cfg.DBPath)
	store.SetMaxVectorSearchCandidates(cfg.MaxVectorCandidates)
	store.SetHNSWParams(cfg.HNSWM, cfg.HNSWEfSearch)
	if cfg.KeywordWeight > 0 || cfg.VectorWeight > 0 {
		store.SetWeightOverrides(float32(cfg.KeywordWeight), float32(cfg.VectorWeight))
	}

	if cfg.RerankerType == "cross-encoder" {
		rerankerModel := cfg.EffectiveRerankerModel()
		reranker := index.NewCrossEncoderReranker(index.CrossEncoderConfig{
			Completer:   featureCompleter,
			Model:       rerankerModel,
			TopK:        20,
			ScoreWeight: 0.5,
		})
		store.SetReranker(reranker)
		logx.Info("cross-encoder reranker enabled", "model", rerankerModel)
	}

	if rawEmbedder != nil {
		rebuild, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{
			Model:      cfg.EmbedModel,
			Dimensions: rawEmbedder.Dimensions(),
			Normalized: true,
		})
		if err != nil {
			return nil, fmt.Errorf("error configuring embedding metadata: %w", err)
		}
		if rebuild {
			logx.Info("embedding metadata changed; rebuilding index from filesystem projection")
		}
	}

	return store, nil
}

func newMainIndexer(cfg *config.Config, store *index.Store, rawEmbedder embed.Embedder, featureCompleter llm.Completer) *Indexer {
	idx := NewIndexer(IndexerConfig{
		Cfg:        cfg,
		Store:      store,
		HNSWStore:  store,
		Embedder:   rawEmbedder,
		Extractor:  extract.NewRouter(extract.Options{PDFOCRLang: cfg.PDFOCRLang, PDFOCRTimeout: cfg.PDFOCRTimeout}),
		Quarantine: store,
		DedupStore: store,
	})

	if cfg.SummarizerEnabled {
		summModel := cfg.EffectiveSummarizerModel()
		summarizer := index.NewChunkSummarizer(index.SummarizerConfig{
			Completer: featureCompleter,
			Model:     summModel,
		})
		idx.pipeline.Summarizer = newSummarizerAdapter(summarizer)
		logx.Info("chunk summarizer enabled", "model", summModel)
	}

	return idx
}

func newMainWatcher(cfg *config.Config) (*watch.Watcher, error) {
	gi, err := scan.LoadGitIgnore(cfg.WatchDir)
	if err != nil {
		return nil, fmt.Errorf("error loading gitignore: %w", err)
	}

	watcher, err := watch.New(cfg.WatchDir, gi, watch.Options{EventBuffer: cfg.WatchEventBuffer})
	if err != nil {
		return nil, fmt.Errorf("error starting watcher: %w", err)
	}
	return watcher, nil
}
