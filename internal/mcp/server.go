package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type Server struct {
	cfg      *config.Config
	store    index.Searcher
	embedder embed.Embedder
	version  string
	mcp      *mcpserver.MCPServer
	state    *runtimestate.IndexStateTracker

	toolLimiterOnce sync.Once
	toolLimiter     chan struct{}
	maxToolSlots    int
}

const (
	shutdownTimeout        = 5 * time.Second
	readHeaderTimeout      = 5 * time.Second
	healthPath             = "/healthz"
	readinessPath          = "/readyz"
	httpMCPPath            = "/mcp"
	ssePath                = "/sse"
	sseMessagePath         = "/message"
	maxConcurrentToolCalls = 4
)

func NewServer(cfg *config.Config, store index.Searcher, embedder embed.Embedder, version string, state *runtimestate.IndexStateTracker) *Server {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}

	maxTools := cfg.MaxConcurrentTools
	if maxTools < 1 {
		maxTools = maxConcurrentToolCalls
	}

	s := &Server{
		cfg:          cfg,
		store:        store,
		embedder:     embedder,
		version:      version,
		state:        state,
		maxToolSlots: maxTools,
	}

	s.mcp = mcpserver.NewMCPServer("quant", version)
	s.registerTools()

	return s
}

type shutdownable interface {
	Start(addr string) error
	Shutdown(ctx context.Context) error
}

func (s *Server) Serve(ctx context.Context, cfg *config.Config) error {
	switch cfg.Transport {
	case config.TransportStdio:
		stdioServer := mcpserver.NewStdioServer(s.mcp)
		return stdioServer.Listen(ctx, os.Stdin, os.Stdout)
	case config.TransportSSE:
		sseServer, _ := s.newSSEServer(cfg.ListenAddr)
		return s.serveWithShutdown(ctx, sseServer, cfg.ListenAddr)
	case config.TransportHTTP:
		httpServer, _ := s.newStreamableHTTPServer(cfg.ListenAddr)
		return s.serveWithShutdown(ctx, httpServer, cfg.ListenAddr)
	default:
		return fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}

func (s *Server) newStreamableHTTPServer(addr string) (*mcpserver.StreamableHTTPServer, *http.Server) {
	mux := http.NewServeMux()
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	streamServer := mcpserver.NewStreamableHTTPServer(s.mcp, mcpserver.WithStreamableHTTPServer(httpServer))
	mux.Handle(httpMCPPath, streamServer)
	s.registerHealthRoutes(mux)
	return streamServer, httpServer
}

func (s *Server) newSSEServer(addr string) (*mcpserver.SSEServer, *http.Server) {
	mux := http.NewServeMux()
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	sseServer := mcpserver.NewSSEServer(s.mcp, mcpserver.WithHTTPServer(httpServer))
	mux.Handle(ssePath, sseServer.SSEHandler())
	mux.Handle(sseMessagePath, sseServer.MessageHandler())
	s.registerHealthRoutes(mux)
	return sseServer, httpServer
}

func (s *Server) registerHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc(readinessPath, s.handleReadiness)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeProbeResponse(w, r, http.StatusOK, "ok\n")
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if err := s.readinessError(r.Context()); err != nil {
		s.writeProbeResponse(w, r, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	s.writeProbeResponse(w, r, http.StatusOK, "ready\n")
}

func (s *Server) writeProbeResponse(w http.ResponseWriter, r *http.Request, status int, body string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(body))
}

func (s *Server) readinessError(ctx context.Context) error {
	if s.store == nil {
		return errors.New("index store is unavailable")
	}
	if err := s.store.PingContext(ctx); err != nil {
		return fmt.Errorf("index store is unavailable: %w", err)
	}
	if s.embedder == nil {
		return errors.New("embedder is unavailable")
	}
	if s.state != nil {
		snapshot := s.state.Snapshot()
		if !snapshot.Ready() {
			if snapshot.State == "" {
				return errors.New("index is not ready")
			}
			if snapshot.Message != "" {
				return fmt.Errorf("index is %s: %s", snapshot.State, snapshot.Message)
			}
			return fmt.Errorf("index is %s", snapshot.State)
		}
	}
	return nil
}

func (s *Server) serveWithShutdown(ctx context.Context, srv shutdownable, addr string) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(addr) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) acquireToolSlot(ctx context.Context) error {
	s.toolLimiterOnce.Do(func() {
		max := s.maxToolSlots
		if max < 1 {
			max = maxConcurrentToolCalls
		}
		s.toolLimiter = make(chan struct{}, max)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.toolLimiter <- struct{}{}:
		return nil
	}
}

func (s *Server) releaseToolSlot() {
	if s == nil || s.toolLimiter == nil {
		return
	}

	<-s.toolLimiter
}
