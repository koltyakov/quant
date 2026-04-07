package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/andrew/quant/internal/chunk"
	"github.com/andrew/quant/internal/config"
	"github.com/andrew/quant/internal/embed"
	"github.com/andrew/quant/internal/extract"
	"github.com/andrew/quant/internal/index"
	"github.com/andrew/quant/internal/mcp"
	"github.com/andrew/quant/internal/scan"
	"github.com/andrew/quant/internal/watch"
	ignore "github.com/sabhiram/go-gitignore"
)

type indexAction string

const (
	indexNoop    indexAction = "noop"
	indexUpdated indexAction = "updated"
	indexRemoved indexAction = "removed"
)

func main() {
	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	embedder, err := embed.NewOllama(cfg.EmbedURL, cfg.EmbedModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to ollama: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := embedder.Close(); err != nil {
			log.Printf("Error closing embedder: %v", err)
		}
	}()

	log.Printf("Connected to embedding backend via Ollama (model: %s, dimensions: %d)", cfg.EmbedModel, embedder.Dimensions())

	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("Error closing store: %v", err)
		}
	}()

	log.Printf("Database opened: %s", cfg.DBPath)

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{
		Model:      cfg.EmbedModel,
		Dimensions: embedder.Dimensions(),
		Normalized: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error configuring embedding metadata: %v\n", err)
		os.Exit(1)
	}
	if rebuild {
		log.Printf("Embedding metadata changed; rebuilding index from filesystem projection")
	}

	ignore, err := scan.LoadGitIgnore(cfg.WatchDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading gitignore: %v\n", err)
		os.Exit(1)
	}

	extractor := extract.NewRouter()

	log.Printf("Starting initial scan of %s", cfg.WatchDir)
	if err := initialSync(ctx, cfg, store, embedder, extractor, ignore); err != nil {
		fmt.Fprintf(os.Stderr, "error during initial scan: %v\n", err)
		os.Exit(1)
	}

	docCount, chunkCount, _ := store.Stats(ctx)
	log.Printf("Initial scan complete: %d documents, %d chunks", docCount, chunkCount)

	watcher, err := watch.New(cfg.WatchDir, ignore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting watcher: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			log.Printf("Error closing watcher: %v", err)
		}
	}()

	go watchLoop(ctx, cfg, store, embedder, extractor, watcher)

	mcpServer := mcp.NewServer(cfg, store, embedder)
	log.Printf("Starting MCP server (transport: %s)", cfg.Transport)

	if err := mcpServer.Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func initialSync(ctx context.Context, cfg *config.Config, store *index.Store, embedder embed.Embedder, extractor extract.Extractor, gi *ignore.GitIgnore) error {
	results, err := scan.Scan(cfg.WatchDir, gi)
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	docs, err := store.ListDocuments(ctx)
	if err != nil {
		return fmt.Errorf("listing indexed documents: %w", err)
	}
	docByPath := make(map[string]*index.Document, len(docs))
	for i := range docs {
		doc := docs[i]
		docByPath[doc.Path] = &doc
	}

	scannedPaths := make(map[string]bool, len(results))
	pending := make([]scan.Result, 0, len(results))
	for _, r := range results {
		scannedPaths[r.Path] = true
		if !extractor.Supports(r.Path) {
			continue
		}
		if doc := docByPath[r.Path]; doc != nil && sameModTime(doc.ModifiedAt, r.ModifiedAt) {
			continue
		}
		pending = append(pending, r)
	}

	type indexResult struct {
		path   string
		action indexAction
		err    error
	}

	workers := cfg.IndexWorkers
	if workers > len(pending) && len(pending) > 0 {
		workers = len(pending)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan scan.Result)
	indexResults := make(chan indexResult, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				action, err := indexFile(ctx, cfg, store, embedder, extractor, r.Path, r.ModifiedAt)
				indexResults <- indexResult{path: r.Path, action: action, err: err}
			}
		}()
	}

	go func() {
		for _, r := range pending {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				close(indexResults)
				return
			case jobs <- r:
			}
		}
		close(jobs)
		wg.Wait()
		close(indexResults)
	}()

	for result := range indexResults {
		if result.err != nil {
			log.Printf("Error indexing %s: %v", result.path, result.err)
			continue
		}
		switch result.action {
		case indexUpdated:
			log.Printf("Indexed: %s", result.path)
		case indexRemoved:
			log.Printf("Removed from index: %s", result.path)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			if err := store.DeleteDocument(ctx, doc.Path); err != nil {
				log.Printf("Error removing stale document %s: %v", doc.Path, err)
				continue
			}
			log.Printf("Removed from index: %s", doc.Path)
		}
	}

	return nil
}

func watchLoop(ctx context.Context, cfg *config.Config, store *index.Store, embedder embed.Embedder, extractor extract.Extractor, watcher *watch.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events():
			if !ok {
				return
			}

			switch event.Op {
			case watch.Create, watch.Write:
				info, err := os.Stat(event.Path)
				if err != nil {
					continue
				}
				if info.IsDir() {
					continue
				}

				action, err := indexFile(ctx, cfg, store, embedder, extractor, event.Path, info.ModTime())
				if err != nil {
					log.Printf("Error indexing %s: %v", event.Path, err)
					continue
				}
				switch action {
				case indexUpdated:
					log.Printf("Indexed: %s", event.Path)
				case indexRemoved:
					log.Printf("Removed from index: %s", event.Path)
				}

			case watch.Remove:
				if err := store.DeleteDocument(ctx, event.Path); err != nil {
					log.Printf("Error removing %s: %v", event.Path, err)
					continue
				}
				log.Printf("Removed from index: %s", event.Path)
			}
		}
	}
}

func indexFile(ctx context.Context, cfg *config.Config, store *index.Store, embedder embed.Embedder, extractor extract.Extractor, path string, modTime time.Time) (indexAction, error) {
	if !extractor.Supports(path) {
		return indexNoop, nil
	}

	doc, err := store.GetDocumentByPath(ctx, path)
	if err != nil {
		return indexNoop, fmt.Errorf("loading indexed document: %w", err)
	}
	if doc != nil && sameModTime(doc.ModifiedAt, modTime) {
		return indexNoop, nil
	}

	hash, err := scan.FileHash(path)
	if err != nil {
		return indexNoop, fmt.Errorf("hashing file: %w", err)
	}
	if doc != nil && doc.Hash == hash {
		return indexNoop, nil
	}

	text, err := extractor.Extract(ctx, path)
	if err != nil {
		return indexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, store, doc, path)
	}

	chunks := chunk.Split(text, cfg.ChunkSize, cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, store, doc, path)
	}

	indexedDoc := &index.Document{
		Path:       path,
		Hash:       hash,
		ModifiedAt: modTime,
	}

	const embedBatchSize = 16
	chunkRecords := make([]index.ChunkRecord, 0, len(chunks))

	for batchStart := 0; batchStart < len(chunks); batchStart += embedBatchSize {
		batchEnd := batchStart + embedBatchSize
		if batchEnd > len(chunks) {
			batchEnd = len(chunks)
		}

		batch := chunks[batchStart:batchEnd]
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}

		embeddings, err := embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return indexNoop, fmt.Errorf("embedding chunks %d-%d: %w", batchStart, batchEnd-1, err)
		}

		for i, c := range batch {
			if i >= len(embeddings) {
				break
			}
			chunkRecords = append(chunkRecords, index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  index.EncodeFloat32(index.NormalizeFloat32(embeddings[i])),
			})
		}
	}

	if err := store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return indexNoop, err
	}

	return indexUpdated, nil
}

func sameModTime(a, b time.Time) bool {
	return normalizeModTime(a).UnixMicro() == normalizeModTime(b).UnixMicro()
}

func normalizeModTime(t time.Time) time.Time {
	return t.UTC().Round(0)
}

func removeDocumentIfPresent(ctx context.Context, store *index.Store, doc *index.Document, path string) (indexAction, error) {
	if doc == nil {
		return indexNoop, nil
	}
	if err := store.DeleteDocument(ctx, path); err != nil {
		return indexNoop, fmt.Errorf("deleting empty document: %w", err)
	}
	return indexRemoved, nil
}
