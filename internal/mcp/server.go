package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
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

// Server wraps the MCP protocol server, exposing search and index tools over stdio, SSE, or HTTP.
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
	readTimeout            = 15 * time.Second
	idleTimeout            = 60 * time.Second
	maxHeaderBytes         = 64 << 10
	healthPath             = "/healthz"
	readinessPath          = "/readyz"
	httpMCPPath            = "/mcp"
	ssePath                = "/sse"
	sseMessagePath         = "/message"
	maxConcurrentToolCalls = 4
	maxMCPRequestBodyBytes = 1 << 20
)

// NewServer creates an MCP server wired to the given store and embedder.
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

	s.mcp = mcpserver.NewMCPServer("quant", version,
		mcpserver.WithRecovery(),
		mcpserver.WithInputSchemaValidation(),
	)
	s.registerTools()

	return s
}

type shutdownable interface {
	Start(addr string) error
	Shutdown(ctx context.Context) error
}

// Serve starts the MCP server using the transport specified in cfg (stdio, SSE, or HTTP).
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
	httpServer := newHTTPServer(addr, mux)
	streamServer := mcpserver.NewStreamableHTTPServer(s.mcp, mcpserver.WithStreamableHTTPServer(httpServer))
	mux.Handle(httpMCPPath, withTransportSecurity(withRequestBodyLimit(streamServer, maxMCPRequestBodyBytes), s.cfg.MCPToken))
	s.registerHealthRoutes(mux)
	return streamServer, httpServer
}

func (s *Server) newSSEServer(addr string) (*mcpserver.SSEServer, *http.Server) {
	mux := http.NewServeMux()
	httpServer := newHTTPServer(addr, mux)
	sseServer := mcpserver.NewSSEServer(s.mcp, mcpserver.WithHTTPServer(httpServer))
	mux.Handle(ssePath, withTransportSecurity(sseServer.SSEHandler(), s.cfg.MCPToken))
	mux.Handle(sseMessagePath, withTransportSecurity(withRequestBodyLimit(sseServer.MessageHandler(), maxMCPRequestBodyBytes), s.cfg.MCPToken))
	s.registerHealthRoutes(mux)
	return sseServer, httpServer
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

func withTransportSecurity(next http.Handler, token string) http.Handler {
	return withOriginProtection(withBearerAuth(next, token))
}

func withBearerAuth(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="quant"`)
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) registerHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc(readinessPath, s.handleReadiness)
}

// withOriginProtection blocks browser-originated requests. Quant's HTTP
// transports are intended for native MCP clients, which omit Origin; rejecting
// every present Origin prevents websites from driving a loopback MCP server.
func withOriginProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Origin")) > 0 {
			http.Error(w, "browser origins are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withRequestBodyLimit(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > limit {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
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
	if s.state != nil {
		snapshot := s.state.Snapshot()
		if !snapshot.Servable() {
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
