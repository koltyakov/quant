package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/lock"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/mcp"
	"github.com/koltyakov/quant/internal/proxy"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

var ErrRestartRequired = errors.New("restart required")

type AutoUpdateHooks struct {
	Enabled      func() bool
	CheckOnStart func(ctx context.Context, currentVersion string) bool
	StartLoop    func(ctx context.Context, currentVersion string, onUpdate func())
}

func generateInstanceID() string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func RunMCP(ctx context.Context, cfg *config.Config, version string, hooks AutoUpdateHooks) error {
	configureProcessMemory()

	if hooks.Enabled != nil && hooks.Enabled() {
		if hooks.CheckOnStart != nil && hooks.CheckOnStart(ctx, version) {
			return ErrRestartRequired
		}
	}

	if cfg.NoLock {
		return runMain(ctx, cfg, version, hooks, nil)
	}

	instanceID := generateInstanceID()

	if cfg.ProxyAddr != "" {
		return runWorker(ctx, cfg, version, cfg.ProxyAddr)
	}

	lk, err := lock.TryAcquire(cfg.WatchDir, instanceID, "")
	if err == nil {
		return runMain(ctx, cfg, version, hooks, lk)
	}
	if !errors.Is(err, lock.ErrLockHeld) {
		return fmt.Errorf("acquiring lock: %w", err)
	}

	info, readErr := lock.ReadLock(cfg.WatchDir)
	if readErr != nil || info.ProxyAddr == "" {
		logx.Warn("lock held but no proxy address found; waiting and retrying", "err", readErr)
		return waitForLockAndRun(ctx, cfg, version, hooks, instanceID)
	}

	if !lock.CheckMainAlive(cfg.WatchDir) {
		lk, err = lock.TryAcquire(cfg.WatchDir, instanceID, "")
		if err == nil {
			return runMain(ctx, cfg, version, hooks, lk)
		}
	}

	logx.Info("another instance is main; starting as worker", "main_addr", info.ProxyAddr, "main_pid", info.PID)
	return runWorker(ctx, cfg, version, info.ProxyAddr)
}

func waitForLockAndRun(ctx context.Context, cfg *config.Config, version string, hooks AutoUpdateHooks, instanceID string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !lock.CheckMainAlive(cfg.WatchDir) {
				lk, err := lock.TryAcquire(cfg.WatchDir, instanceID, "")
				if err == nil {
					return runMain(ctx, cfg, version, hooks, lk)
				}
			}
			info, readErr := lock.ReadLock(cfg.WatchDir)
			if readErr == nil && info.ProxyAddr != "" {
				logx.Info("main instance discovered; starting as worker", "main_addr", info.ProxyAddr)
				return runWorker(ctx, cfg, version, info.ProxyAddr)
			}
		}
	}
}

func runMain(ctx context.Context, cfg *config.Config, version string, hooks AutoUpdateHooks, lk *lock.Lock) error {
	if lk != nil {
		defer func() {
			if err := lk.Release(); err != nil {
				logx.Error("releasing lock failed", "err", err)
			}
		}()
	}

	rawEmbedder, err := embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel)
	if err != nil && isLocalURL(cfg.EmbedURL) {
		if ollamaPath, lookErr := exec.LookPath("ollama"); lookErr == nil {
			// Step 1: start Ollama if it isn't running.
			if errors.Is(err, embed.ErrOllamaUnavailable) {
				logx.Info("Ollama not running; attempting to start it automatically", "binary", ollamaPath)
				if startErr := autoStartOllama(ctx); startErr != nil {
					logx.Warn("auto-start Ollama failed", "err", startErr)
				} else {
					rawEmbedder, err = embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel)
				}
			}
			// Step 2: pull the model if Ollama is running but the model isn't present.
			if errors.Is(err, embed.ErrModelNotFound) {
				logx.Info("embedding model not found; pulling from Ollama (this may take a while)", "model", cfg.EmbedModel)
				if pullErr := pullOllamaModel(ctx, cfg.EmbedModel); pullErr != nil {
					logx.Warn("auto-pull model failed", "model", cfg.EmbedModel, "err", pullErr)
				} else {
					rawEmbedder, err = embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel)
				}
			}
		}
	}
	if err != nil {
		if !errors.Is(err, embed.ErrOllamaUnavailable) && !errors.Is(err, embed.ErrModelNotFound) {
			return fmt.Errorf("error connecting to embedding backend: %w", err)
		}
		logx.Warn("embedding backend unavailable; MCP will start in keyword-only mode — run 'ollama serve' to enable vector search", "err", err)
	}

	var embedder embed.Embedder
	if rawEmbedder != nil {
		defer func() {
			if err := rawEmbedder.Close(); err != nil {
				logx.Error("closing embedder failed", "err", err)
			}
		}()
		logx.Info("connected to embedding backend", "provider", cfg.EmbedProvider, "model", cfg.EmbedModel, "dimensions", rawEmbedder.Dimensions())
		embedder = embed.NewCachingEmbedder(rawEmbedder, embed.CachingConfig{
			CacheSize:           128,
			CircuitFailureLimit: 5,
			CircuitResetTimeout: 30 * time.Second,
			NormalizeFunc:       index.NormalizeFloat32,
		})
	}

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
	store.SetHNSWParams(cfg.HNSWM, cfg.HNSWEfSearch)
	if cfg.KeywordWeight > 0 || cfg.VectorWeight > 0 {
		store.SetWeightOverrides(float32(cfg.KeywordWeight), float32(cfg.VectorWeight))
	}

	if cfg.RerankerType == "cross-encoder" && cfg.RerankerModel != "" {
		reranker := index.NewCrossEncoderReranker(index.CrossEncoderConfig{
			BaseURL:     cfg.EmbedURL,
			Model:       cfg.RerankerModel,
			TopK:        20,
			ScoreWeight: 0.5,
		})
		store.SetReranker(reranker)
		logx.Info("cross-encoder reranker enabled", "model", cfg.RerankerModel)
	}

	if rawEmbedder != nil {
		rebuild, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{
			Model:      cfg.EmbedModel,
			Dimensions: rawEmbedder.Dimensions(),
			Normalized: true,
		})
		if err != nil {
			return fmt.Errorf("error configuring embedding metadata: %w", err)
		}
		if rebuild {
			logx.Info("embedding metadata changed; rebuilding index from filesystem projection")
		}
	}

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
		summModel := cfg.SummarizerModel
		if summModel == "" {
			summModel = cfg.EmbedModel
		}
		summarizer := index.NewChunkSummarizer(index.SummarizerConfig{
			BaseURL: cfg.EmbedURL,
			Model:   summModel,
		})
		idx.pipeline.Summarizer = newSummarizerAdapter(summarizer)
		logx.Info("chunk summarizer enabled", "model", summModel)
	}

	proxyServer := proxy.NewServer(store, idx.IndexState)
	proxyAddr, err := proxyServer.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting proxy server: %w", err)
	}

	if lk != nil {
		lk.UpdateProxyAddr(proxyAddr)
	}

	gi, err := scan.LoadGitIgnore(cfg.WatchDir)
	if err != nil {
		return fmt.Errorf("error loading gitignore: %w", err)
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

	if cfg.HNSWReoptimizeThreshold > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx.RunHNSWReoptimizer(serverCtx, store, cfg.HNSWReoptimizeThreshold)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.RunHNSWPeriodicFlush(serverCtx, store)
	}()

	mcpServer := mcp.NewServer(cfg, store, embedder, version, idx.IndexState)
	logx.Info("starting MCP server (main)", "transport", cfg.Transport, "proxy_addr", proxyAddr)

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

func runWorker(ctx context.Context, cfg *config.Config, version string, mainAddr string) error {
	client := proxy.NewClient(mainAddr)

	if !client.Alive(ctx) {
		logx.Warn("main process not reachable; attempting to become main", "addr", mainAddr)
		instanceID := generateInstanceID()
		if !lock.CheckMainAlive(cfg.WatchDir) {
			lk, err := lock.TryAcquire(cfg.WatchDir, instanceID, "")
			if err == nil {
				return runMain(ctx, cfg, version, AutoUpdateHooks{}, lk)
			}
		}
		return fmt.Errorf("main process unreachable at %s and cannot acquire lock", mainAddr)
	}

	var workerEmbedder embed.Embedder
	rawEmbedder, err := embed.NewEmbedder(ctx, embed.ProviderType(cfg.EmbedProvider), cfg.EmbedURL, cfg.EmbedModel)
	if err != nil {
		logx.Warn("worker cannot connect to embedding backend; search will be proxy-only", "err", err)
	} else {
		workerEmbedder = rawEmbedder
		defer func() {
			if err := rawEmbedder.Close(); err != nil {
				logx.Error("closing worker embedder failed", "err", err)
			}
		}()
	}

	logx.Info("starting MCP server (worker)", "transport", cfg.Transport, "main_addr", mainAddr)

	var wg sync.WaitGroup
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	var promoted atomic.Bool

	wg.Add(1)
	go func() {
		defer wg.Done()
		watchMainAndPromote(serverCtx, cfg, version, client, mainAddr, serverCancel, &promoted)
	}()

	mcpServer := mcp.NewServer(cfg, client, workerEmbedder, version, nil)

	if err := mcpServer.Serve(serverCtx, cfg); err != nil {
		serverCancel()
		wg.Wait()
		if promoted.Load() {
			return ErrRestartRequired
		}
		return err
	}

	serverCancel()
	wg.Wait()
	if promoted.Load() {
		return ErrRestartRequired
	}
	return nil
}

// isLocalURL reports whether rawURL targets a loopback address (localhost or 127.x or ::1).
// Used to decide whether auto-starting a local Ollama instance makes sense.
func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// autoStartOllama runs "ollama serve" in the background and waits up to ~7 s for
// the process to become reachable. The subprocess is intentionally not tracked —
// it continues running after the caller exits, just like a user-launched instance.
func autoStartOllama(ctx context.Context) error {
	cmd := exec.Command("ollama", "serve") //nolint:gosec
	cmd.Stdout = nil
	cmd.Stderr = nil
	detachProcess(cmd) // own process group — survives Ctrl+C on the quant terminal
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching ollama serve: %w", err)
	}

	// Poll until the process answers or the budget expires.
	for range 5 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		// A HEAD request to /api/tags is the lightest possible liveness check.
		if err := ollamaLive(ctx, "http://localhost:11434"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("ollama did not become ready after 7.5 s")
}

// pullOllamaModel runs "ollama pull <model>" to completion, streaming its
// progress output to stderr so the user can follow the download.
func pullOllamaModel(ctx context.Context, model string) error {
	cmd := exec.CommandContext(ctx, "ollama", "pull", model) //nolint:gosec
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ollama pull %s: %w", model, err)
	}
	return nil
}

func ollamaLive(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func watchMainAndPromote(ctx context.Context, cfg *config.Config, version string, client *proxy.Client, mainAddr string, cancel context.CancelFunc, promoted *atomic.Bool) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if client.Alive(ctx) {
				continue
			}

			logx.Warn("main process lost; attempting to become main")
			if !lock.CheckMainAlive(cfg.WatchDir) {
				instanceID := generateInstanceID()
				lk, err := lock.TryAcquire(cfg.WatchDir, instanceID, "")
				if err == nil {
					logx.Info("worker promoted to main")
					_ = lk.Release()
					promoted.Store(true)
					cancel()
					return
				}
			}

			info, readErr := lock.ReadLock(cfg.WatchDir)
			if readErr == nil && info.ProxyAddr != "" && info.ProxyAddr != mainAddr {
				logx.Info("new main detected; current worker should be restarted", "new_addr", info.ProxyAddr)
			}
		}
	}
}
