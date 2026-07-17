package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/lock"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/mcp"
	"github.com/koltyakov/quant/internal/proxy"
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

func mainConfigFingerprint(cfg *config.Config) string {
	watchDir := cfg.WatchDir
	if absPath, err := filepath.Abs(watchDir); err == nil {
		watchDir = filepath.Clean(absPath)
	}
	if resolved, err := filepath.EvalSymlinks(watchDir); err == nil {
		watchDir = filepath.Clean(resolved)
	}
	payload := struct {
		WatchDir            string
		EmbedURL            string
		EmbedModel          string
		EmbedProvider       string
		LLMURL              string
		LLMModel            string
		LLMProvider         string
		EmbedBatchSize      int
		PDFOCRLang          string
		PDFOCRTimeout       time.Duration
		ChunkSize           int
		ChunkOverlap        float64
		IncludePatterns     []string
		ExcludePatterns     []string
		MaxVectorCandidates int
		KeywordWeight       float64
		VectorWeight        float64
		HNSWM               int
		HNSWEfSearch        int
		RerankerType        string
		RerankerModel       string
		SummarizerEnabled   bool
		SummarizerModel     string
	}{
		WatchDir:            watchDir,
		EmbedURL:            cfg.EmbedURL,
		EmbedModel:          cfg.EmbedModel,
		EmbedProvider:       cfg.EmbedProvider,
		LLMURL:              cfg.LLMURL,
		LLMModel:            cfg.LLMModel,
		LLMProvider:         cfg.LLMProvider,
		EmbedBatchSize:      cfg.EmbedBatchSize,
		PDFOCRLang:          cfg.PDFOCRLang,
		PDFOCRTimeout:       cfg.PDFOCRTimeout,
		ChunkSize:           cfg.ChunkSize,
		ChunkOverlap:        cfg.ChunkOverlap,
		IncludePatterns:     cfg.IncludePatterns,
		ExcludePatterns:     cfg.ExcludePatterns,
		MaxVectorCandidates: cfg.MaxVectorCandidates,
		KeywordWeight:       cfg.KeywordWeight,
		VectorWeight:        cfg.VectorWeight,
		HNSWM:               cfg.HNSWM,
		HNSWEfSearch:        cfg.HNSWEfSearch,
		RerankerType:        cfg.RerankerType,
		RerankerModel:       cfg.RerankerModel,
		SummarizerEnabled:   cfg.SummarizerEnabled,
		SummarizerModel:     cfg.SummarizerModel,
	}
	data, _ := json.Marshal(payload)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func tryAcquireMainLock(cfg *config.Config, instanceID, proxyAddr string) (*lock.Lock, error) {
	return lock.TryAcquireForDBWithFingerprint(cfg.DBPath, instanceID, proxyAddr, mainConfigFingerprint(cfg))
}

func validateMainLockConfig(cfg *config.Config, info *lock.LockInfo) error {
	if info == nil || info.ConfigFingerprint == "" || info.ConfigFingerprint == mainConfigFingerprint(cfg) {
		return nil
	}
	return fmt.Errorf("database %s is already served with an incompatible indexing configuration", cfg.DBPath)
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

	lk, err := tryAcquireMainLock(cfg, instanceID, "")
	if err == nil {
		return runMain(ctx, cfg, version, hooks, lk)
	}
	if !errors.Is(err, lock.ErrLockHeld) {
		return fmt.Errorf("acquiring lock: %w", err)
	}

	info, readErr := lock.ReadLockForDB(cfg.DBPath)
	if readErr == nil {
		if err := validateMainLockConfig(cfg, info); err != nil {
			return err
		}
	}
	if readErr != nil || info.ProxyAddr == "" {
		logx.Warn("lock held but no proxy address found; waiting and retrying", "err", readErr)
		return waitForLockAndRun(ctx, cfg, version, hooks, instanceID)
	}

	if !lock.CheckMainAliveForDB(cfg.DBPath) {
		lk, err = tryAcquireMainLock(cfg, instanceID, "")
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
			if !lock.CheckMainAliveForDB(cfg.DBPath) {
				lk, err := tryAcquireMainLock(cfg, instanceID, "")
				if err == nil {
					return runMain(ctx, cfg, version, hooks, lk)
				}
			}
			info, readErr := lock.ReadLockForDB(cfg.DBPath)
			if readErr == nil {
				if err := validateMainLockConfig(cfg, info); err != nil {
					return err
				}
			}
			if readErr == nil && info.ProxyAddr != "" {
				logx.Info("main instance discovered; starting as worker", "main_addr", info.ProxyAddr)
				return runWorker(ctx, cfg, version, info.ProxyAddr)
			}
		}
	}
}

func runMain(ctx context.Context, cfg *config.Config, version string, hooks AutoUpdateHooks, lk *lock.Lock) error {
	proc, err := newMainProcess(ctx, cfg, version, lk)
	if err != nil {
		return err
	}
	defer proc.Close()

	return proc.Serve(hooks)
}

func runWorker(ctx context.Context, cfg *config.Config, version string, mainAddr string) error {
	client := proxy.NewClient(mainAddr)

	if !client.Alive(ctx) {
		logx.Warn("main process not reachable; attempting to become main", "addr", mainAddr)
		instanceID := generateInstanceID()
		if !lock.CheckMainAliveForDB(cfg.DBPath) {
			lk, err := tryAcquireMainLock(cfg, instanceID, "")
			if err == nil {
				return runMain(ctx, cfg, version, AutoUpdateHooks{}, lk)
			}
		}
		return fmt.Errorf("main process unreachable at %s and cannot acquire lock", mainAddr)
	}

	logx.Info("starting MCP server (worker)", "transport", cfg.Transport, "main_addr", mainAddr)

	runner := newProcessRunner(ctx)
	runner.Go(func() { watchMainAndPromote(runner.ctx, cfg, client, mainAddr, runner.requestRestart) })
	mcpServer := mcp.NewServer(cfg, client, nil, version, nil)
	return runner.Run(mcpServer, cfg)
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

type mainAliveChecker interface {
	Alive(ctx context.Context) bool
}

func watchMainAndPromote(ctx context.Context, cfg *config.Config, client mainAliveChecker, mainAddr string, onRestart func()) {
	watchMainAndPromoteInterval(ctx, cfg, client, mainAddr, onRestart, 5*time.Second)
}

func watchMainAndPromoteInterval(ctx context.Context, cfg *config.Config, client mainAliveChecker, mainAddr string, onRestart func(), interval time.Duration) {
	ticker := time.NewTicker(interval)
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
			if !lock.CheckMainAliveForDB(cfg.DBPath) {
				instanceID := generateInstanceID()
				lk, err := tryAcquireMainLock(cfg, instanceID, "")
				if err == nil {
					logx.Info("worker promoted to main")
					_ = lk.Release()
					if onRestart != nil {
						onRestart()
					}
					return
				}
			}

			info, readErr := lock.ReadLockForDB(cfg.DBPath)
			if readErr == nil && info.ProxyAddr != "" && info.ProxyAddr != mainAddr {
				if err := validateMainLockConfig(cfg, info); err != nil {
					logx.Error("new main has incompatible indexing configuration", "err", err)
				}
				logx.Info("new main detected; current worker restarting", "new_addr", info.ProxyAddr)
				if onRestart != nil {
					onRestart()
				}
				return
			}
		}
	}
}
