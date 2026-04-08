package mcp

import (
	"container/list"
	"context"
	"fmt"
	"os"
	"sync"

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

	embCacheMu sync.Mutex
	embCache   *embeddingLRU
	embFlights map[string]*embeddingFlight
}

const embCacheMaxSize = 128

func NewServer(cfg *config.Config, store *index.Store, embedder embed.Embedder) *Server {
	s := &Server{
		cfg:        cfg,
		store:      store,
		embedder:   embedder,
		embCache:   newEmbeddingLRU(embCacheMaxSize),
		embFlights: make(map[string]*embeddingFlight),
	}

	s.mcp = mcpserver.NewMCPServer("quant", "1.0.0")
	s.registerTools()

	return s
}

type embeddingCacheEntry struct {
	key   string
	value []float32
}

type embeddingLRU struct {
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type embeddingFlight struct {
	done    chan struct{}
	waiters int
	vec     []float32
	err     error
}

func newEmbeddingLRU(capacity int) *embeddingLRU {
	if capacity < 1 {
		capacity = 1
	}
	return &embeddingLRU{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

func (c *embeddingLRU) Get(key string) ([]float32, bool) {
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(elem)
	return elem.Value.(*embeddingCacheEntry).value, true
}

func (c *embeddingLRU) Put(key string, value []float32) {
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*embeddingCacheEntry)
		entry.value = value
		c.ll.MoveToFront(elem)
		return
	}

	elem := c.ll.PushFront(&embeddingCacheEntry{key: key, value: value})
	c.items[key] = elem
	if c.ll.Len() <= c.capacity {
		return
	}

	tail := c.ll.Back()
	if tail == nil {
		return
	}
	c.ll.Remove(tail)
	delete(c.items, tail.Value.(*embeddingCacheEntry).key)
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
