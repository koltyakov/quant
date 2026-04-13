package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

type Server struct {
	store    index.Searcher
	state    *runtimestate.IndexStateTracker
	listener net.Listener
	server   *http.Server
	addr     string

	mu    sync.Mutex
	ready bool
}

func NewServer(store index.Searcher, state *runtimestate.IndexStateTracker) *Server {
	return &Server{
		store: store,
		state: state,
	}
}

func (s *Server) Start(ctx context.Context) (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("starting proxy listener: %w", err)
	}
	s.listener = listener
	s.addr = listener.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/search", s.handleSearch)
	mux.HandleFunc("/proxy/find_similar", s.handleFindSimilar)
	mux.HandleFunc("/proxy/list_sources", s.handleListSources)
	mux.HandleFunc("/proxy/index_status", s.handleIndexStatus)
	mux.HandleFunc("/proxy/ping", s.handlePing)
	mux.HandleFunc("/proxy/chunk_by_id", s.handleChunkByID)
	mux.HandleFunc("/proxy/stats", s.handleStats)
	mux.HandleFunc("/proxy/embed", s.handleEmbed)

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logx.Error("proxy server error", "err", err)
		}
	}()

	go func() { //nolint:gosec // G118: intentional context.Background for shutdown after parent ctx cancelled
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}()

	logx.Info("proxy server listening", "addr", s.addr)
	return s.addr, nil
}

func (s *Server) Addr() string {
	return s.addr
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	data, _ := json.Marshal(v)
	_, _ = w.Write(data)
}

func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "reading body")
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid json")
		return false
	}
	return true
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if !s.readBody(w, r, &req) {
		return
	}

	results, err := s.store.Search(r.Context(), req.Query, req.QueryEmbedding, req.Limit, req.PathPrefix)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, SearchResponse{Results: results})
}

func (s *Server) handleFindSimilar(w http.ResponseWriter, r *http.Request) {
	var req FindSimilarRequest
	if !s.readBody(w, r, &req) {
		return
	}

	results, err := s.store.FindSimilar(r.Context(), req.ChunkID, req.Limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, FindSimilarResponse{Results: results})
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	var req ListSourcesRequest
	if !s.readBody(w, r, &req) {
		return
	}

	docs, err := s.store.ListDocumentsLimit(r.Context(), req.Limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	docCount, _, _ := s.store.Stats(r.Context())
	s.writeJSON(w, http.StatusOK, ListSourcesResponse{Documents: docs, Total: docCount})
}

func (s *Server) handleIndexStatus(w http.ResponseWriter, r *http.Request) {
	docCount, chunkCount, err := s.store.Stats(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := IndexStatusResponse{
		Documents: docCount,
		Chunks:    chunkCount,
	}

	if s.state != nil {
		snap := s.state.Snapshot()
		resp.State = string(snap.State)
		resp.StateMessage = snap.Message
		resp.StateUpdatedAt = snap.UpdatedAt
	}

	if provider, ok := s.store.(index.FTSDiagnosticsProvider); ok {
		diag, diagErr := provider.FTSDiagnostics(r.Context())
		if diagErr == nil {
			resp.FTS = &diag
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if err := s.store.PingContext(r.Context()); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, http.StatusNotImplemented, "embed not proxied; workers embed locally")
}

func (s *Server) handleChunkByID(w http.ResponseWriter, r *http.Request) {
	var req ChunkByIDRequest
	if !s.readBody(w, r, &req) {
		return
	}
	result, err := s.store.GetChunkByID(r.Context(), req.ChunkID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, ChunkByIDResponse{Chunk: *result})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	docCount, chunkCount, err := s.store.Stats(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, StatsResponse{DocCount: docCount, ChunkCount: chunkCount})
}
