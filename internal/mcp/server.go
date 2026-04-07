package mcp

import (
	"context"
	"fmt"
	"os"

	"github.com/andrew/quant/internal/config"
	"github.com/andrew/quant/internal/embed"
	"github.com/andrew/quant/internal/index"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type Server struct {
	cfg      *config.Config
	store    *index.Store
	embedder embed.Embedder
	mcp      *mcpserver.MCPServer
}

func NewServer(cfg *config.Config, store *index.Store, embedder embed.Embedder) *Server {
	s := &Server{
		cfg:      cfg,
		store:    store,
		embedder: embedder,
	}

	s.mcp = mcpserver.NewMCPServer("quant", "1.0.0")
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
		return s.serveWithShutdown(ctx, mcpserver.NewSSEServer(s.mcp), cfg.ListenAddr)
	case config.TransportHTTP:
		return s.serveWithShutdown(ctx, mcpserver.NewStreamableHTTPServer(s.mcp), cfg.ListenAddr)
	default:
		return fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}

func (s *Server) serveWithShutdown(ctx context.Context, srv shutdownable, addr string) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(addr) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	}
}
