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

	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

type Server struct {
	store    index.Searcher
	embedder embed.Embedder
	state    *runtimestate.IndexStateTracker
	listener net.Listener
	server   *http.Server
	addr     string

	mu           sync.Mutex
	ready        bool
	shutdownOnce sync.Once
	shutdownErr  error
}

func NewServer(store index.Searcher, state *runtimestate.IndexStateTracker, embedder embed.Embedder) *Server {
	return &Server{
		store:    store,
		embedder: embedder,
		state:    state,
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
	mux.HandleFunc("/proxy/list_collections", s.handleListCollections)
	mux.HandleFunc("/proxy/collection_stats", s.handleCollectionStats)
	mux.HandleFunc("/proxy/delete_collection", s.handleDeleteCollection)

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
		_ = s.Shutdown(shutdownCtx)
	}()

	logx.Info("proxy server listening", "addr", s.addr)
	return s.addr, nil
}

func (s *Server) Addr() string {
	return s.addr
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		server := s.server
		s.ready = false
		s.mu.Unlock()

		if server == nil {
			return
		}
		s.shutdownErr = server.Shutdown(ctx)
	})
	return s.shutdownErr
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
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

	results, err := s.store.SearchFiltered(r.Context(), req.Query, req.QueryEmbedding, req.Limit, req.PathPrefix, req.Filter)
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
	if s.embedder == nil {
		resp.EmbeddingStatus = "unavailable (keyword-only mode) — start Ollama with: ollama serve"
	} else {
		resp.EmbeddingStatus = "available"
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
	var req EmbedRequest
	if !s.readBody(w, r, &req) {
		return
	}
	if s.embedder == nil {
		s.writeError(w, http.StatusServiceUnavailable, "embedding backend unavailable")
		return
	}
	embedding, err := s.embedder.Embed(r.Context(), req.Text)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, EmbedResponse{Embedding: embedding})
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

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	collections, err := s.store.ListCollections(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, ListCollectionsResponse{Collections: collections})
}

func (s *Server) handleCollectionStats(w http.ResponseWriter, r *http.Request) {
	var req CollectionStatsRequest
	if !s.readBody(w, r, &req) {
		return
	}
	docs, chunks, err := s.store.CollectionStats(r.Context(), req.Collection)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, CollectionStatsResponse{Documents: docs, Chunks: chunks})
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	var req DeleteCollectionRequest
	if !s.readBody(w, r, &req) {
		return
	}
	if err := s.store.DeleteCollection(r.Context(), req.Collection); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, DeleteCollectionResponse{Deleted: true})
}
