package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
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
	defer embedder.Close()

	log.Printf("Connected to embedding backend via Ollama (model: %s, dimensions: %d)", cfg.EmbedModel, embedder.Dimensions())

	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

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
	defer watcher.Close()

	go watchLoop(ctx, cfg, store, embedder, extractor, watcher)

	mcpServer := mcp.NewServer(cfg, store, embedder)
	log.Printf("Starting MCP server (transport: %s)", cfg.Transport)

	if err := mcpServer.Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func initialSync(ctx context.Context, cfg *config.Config, store *index.Store, embedder *embed.Ollama, extractor *extract.Router, gi *ignore.GitIgnore) error {
	results, err := scan.Scan(cfg.WatchDir, gi)
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	for _, r := range results {
		indexed, err := indexFile(ctx, cfg, store, embedder, extractor, r.Path, r.ModifiedAt)
		if err != nil {
			log.Printf("Error indexing %s: %v", r.Path, err)
			continue
		}
		if indexed {
			log.Printf("Indexed: %s", r.Path)
		}
	}

	docs, _ := store.ListDocuments(ctx)
	resultMap := make(map[string]bool, len(results))
	for _, r := range results {
		resultMap[r.Path] = true
	}
	for _, doc := range docs {
		if !resultMap[doc.Path] {
			store.DeleteDocument(ctx, doc.Path)
			log.Printf("Removed from index: %s", doc.Path)
		}
	}

	return nil
}

func watchLoop(ctx context.Context, cfg *config.Config, store *index.Store, embedder *embed.Ollama, extractor *extract.Router, watcher *watch.Watcher) {
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

				indexed, err := indexFile(ctx, cfg, store, embedder, extractor, event.Path, info.ModTime())
				if err != nil {
					log.Printf("Error indexing %s: %v", event.Path, err)
					continue
				}
				if indexed {
					log.Printf("Indexed: %s", event.Path)
				}

			case watch.Remove:
				store.DeleteDocument(ctx, event.Path)
				log.Printf("Removed from index: %s", event.Path)
			}
		}
	}
}

func indexFile(ctx context.Context, cfg *config.Config, store *index.Store, embedder *embed.Ollama, extractor *extract.Router, path string, modTime time.Time) (bool, error) {
	if !extractor.Supports(path) {
		return false, nil
	}

	doc, err := store.GetDocumentByPath(ctx, path)
	if err != nil {
		return false, fmt.Errorf("loading indexed document: %w", err)
	}
	if doc != nil && doc.ModifiedAt.Equal(modTime) {
		return false, nil
	}

	hash, err := scan.FileHash(path)
	if err != nil {
		return false, fmt.Errorf("hashing file: %w", err)
	}
	if doc != nil && doc.Hash == hash {
		return false, nil
	}

	text, err := extractor.Extract(ctx, path)
	if err != nil {
		return false, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return false, nil
	}

	chunks := chunk.Split(text, cfg.ChunkSize, cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return false, nil
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
			return false, fmt.Errorf("embedding chunks %d-%d: %w", batchStart, batchEnd-1, err)
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
		return false, err
	}

	return true, nil
}
