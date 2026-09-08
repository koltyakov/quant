package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

type revisionEmbedder struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
	calls   int
}

func (e *revisionEmbedder) Dimensions() int { return 3 }
func (e *revisionEmbedder) Close() error    { return nil }
func (e *revisionEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	result, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return result[0], nil
}
func (e *revisionEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.started != nil {
		e.once.Do(func() { close(e.started) })
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = []float32{1, 0.5, 0}
	}
	return result, nil
}

func revisionIndexer(t *testing.T, root string, store *index.Store, emb embed.Embedder) *Indexer {
	t.Helper()
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = filepath.Join(t.TempDir(), "index.db")
	cfg.EmbedModel = "same-model"
	return NewIndexer(IndexerConfig{Cfg: cfg, Store: store, HNSWStore: store, Embedder: emb, Extractor: extract.NewRouter(extract.Options{}), Quarantine: store, DedupStore: store})
}

func TestIndexerStartupBackfillsSameModelKeywordDocuments(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "searchable keyword text")
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	meta := index.EmbeddingMetadata{Model: "same-model", Dimensions: 3, Normalized: true}
	if _, err := store.EnsureEmbeddingMetadata(ctx, meta); err != nil {
		t.Fatal(err)
	}
	idx := revisionIndexer(t, root, store, nil)
	if err := idx.InitialSync(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDocumentByPath(ctx, "a.txt")
	if err != nil || before == nil {
		t.Fatalf("document: %v %v", before, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = index.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if reset, err := store.EnsureEmbeddingMetadata(ctx, meta); err != nil || reset {
		t.Fatalf("same model reset=%v err=%v", reset, err)
	}
	emb := &revisionEmbedder{}
	idx = revisionIndexer(t, root, store, emb)
	if err := idx.InitialSync(ctx); err != nil {
		t.Fatal(err)
	}
	if emb.calls == 0 {
		t.Fatal("startup did not backfill embeddings")
	}
	chunks, err := store.GetDocumentChunksByPath(ctx, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if !idx.pipeline.UsableEmbedding(c.Embedding) {
			t.Fatal("missing embedding")
		}
	}
	after, _ := store.GetDocumentByPath(ctx, "a.txt")
	if after.Hash != before.Hash {
		t.Fatal("backfill changed source revision")
	}
	calls := emb.calls
	if err := idx.InitialSync(ctx); err != nil {
		t.Fatal(err)
	}
	if emb.calls != calls {
		t.Fatal("complete projection was embedded again")
	}
	idx.cfg.ChunkSize++
	if err := idx.InitialSync(ctx); err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := store.DocumentProjection(ctx, "a.txt")
	if fingerprint.Fingerprint != idx.projectionFingerprint() {
		t.Fatal("chunk configuration change was ignored")
	}
	// A same-dimension model change must miss both document reuse and dedup.
	cfg := config.Default()
	cfg.WatchDir = root
	cfg.DBPath = idx.cfg.DBPath
	cfg.ChunkSize = idx.cfg.ChunkSize
	cfg.EmbedModel = "different-model"
	nextEmbedder := &revisionEmbedder{}
	next := NewIndexer(IndexerConfig{Cfg: cfg, Store: store, HNSWStore: store, Embedder: nextEmbedder, Extractor: idx.extractor, Quarantine: store, DedupStore: store})
	if err := next.InitialSync(ctx); err != nil {
		t.Fatal(err)
	}
	if nextEmbedder.calls == 0 {
		t.Fatal("different model reused old document/dedup vectors")
	}
}

func TestIndexerBlockedPublication(t *testing.T) {
	for _, mutation := range []string{"rewrite", "remove", "prefix", "failure-rewrite", "failure-remove"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			file := filepath.Join(root, "docs", "a.txt")
			writeFile(t, file, "old committed text")
			store, err := index.NewStore(filepath.Join(t.TempDir(), "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			idx := revisionIndexer(t, root, store, nil)
			ref, _ := ResolveDocumentRef(root, file)
			if _, err := idx.SyncDocumentRef(ctx, ref, nil, nil); err != nil {
				t.Fatal(err)
			}
			old, _ := store.GetDocumentByPath(ctx, ref.Key)
			writeFile(t, file, "candidate text")
			before, _ := os.Stat(file)
			emb := &revisionEmbedder{started: make(chan struct{}), release: make(chan struct{})}
			if mutation == "failure-rewrite" || mutation == "failure-remove" {
				emb.err = embed.ErrPermanent
			}
			idx = revisionIndexer(t, root, store, emb)
			done := make(chan error, 1)
			go func() { _, err := idx.SyncDocumentRef(ctx, ref, nil, nil); done <- err }()
			select {
			case <-emb.started:
			case <-time.After(5 * time.Second):
				t.Fatal("embedder did not start")
			}
			current, _ := store.GetDocumentByPath(ctx, ref.Key)
			if current == nil || current.Hash != old.Hash {
				t.Fatal("old document removed before replacement")
			}
			switch mutation {
			case "rewrite", "failure-rewrite":
				// Same size and restored mtime force publication to check the bytes.
				writeFile(t, file, "different text")
				if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "remove", "failure-remove":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				idx.HandleWatchEvent(ctx, watch.Event{Path: file, Op: watch.Remove})
			case "prefix":
				if err := os.RemoveAll(filepath.Dir(file)); err != nil {
					t.Fatal(err)
				}
				idx.HandleWatchEvent(ctx, watch.Event{Path: filepath.Dir(file), Op: watch.Remove, IsDir: true})
			}
			close(emb.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("indexing did not finish")
			}
			current, _ = store.GetDocumentByPath(ctx, ref.Key)
			if mutation == "rewrite" || mutation == "failure-rewrite" {
				if current == nil || current.Hash != old.Hash {
					t.Fatal("stale job replaced/deleted old document")
				}
			} else if current != nil {
				t.Fatal("deleted document was reinserted")
			}
			if q, _ := store.IsQuarantined(ctx, ref.Key); q {
				t.Fatal("stale failure quarantined the new revision")
			}
		})
	}
}

func TestIndexerQuarantineRecovery(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "source revision one")
	store, err := index.NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	emb := &revisionEmbedder{err: embed.ErrPermanent}
	idx := revisionIndexer(t, root, store, emb)
	ref, _ := ResolveDocumentRef(root, file)
	if _, err := idx.SyncDocumentRef(ctx, ref, nil, nil); !errors.Is(err, embed.ErrPermanent) {
		t.Fatalf("err=%v", err)
	}
	q, err := store.GetQuarantineRevision(ctx, ref.Key)
	hash, _ := scan.FileHash(file)
	if err != nil || q == nil || q.Revision != hash || q.Category != "embedding" {
		t.Fatalf("quarantine=%+v err=%v", q, err)
	}
	calls := emb.calls
	if _, err := idx.SyncDocumentRef(ctx, ref, nil, nil); err != nil {
		t.Fatal(err)
	}
	if emb.calls != calls {
		t.Fatal("same failure retried")
	}
	writeFile(t, file, "source revision two")
	_, _ = idx.SyncDocumentRef(ctx, ref, nil, nil)
	if emb.calls == calls {
		t.Fatal("source change did not retry")
	}
	calls = emb.calls
	idx.cfg.EmbedModel = "changed-model"
	_, _ = idx.SyncDocumentRef(ctx, ref, nil, nil)
	if emb.calls == calls {
		t.Fatal("model change did not retry")
	}
	if err := store.RemoveFromQuarantine(ctx, ref.Key); err != nil {
		t.Fatal(err)
	}
	emb.err = nil
	if action, err := idx.SyncDocumentRef(ctx, ref, nil, nil); err != nil || action != IndexUpdated {
		t.Fatalf("explicit retry: %s %v", action, err)
	}
	if q, _ := store.IsQuarantined(ctx, ref.Key); q {
		t.Fatal("quarantine was not cleared")
	}
}

func TestSnapshotExtractsHashedBytes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Dockerfile")
	writeFile(t, file, "FROM original")
	snapshot, hash, _, cleanup, err := snapshotSource(t.Context(), file)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, "FROM changed")
	text, err := extract.NewRouter(extract.Options{}).Extract(t.Context(), snapshot)
	if err != nil || text != "FROM original" {
		t.Fatalf("snapshot extraction=%q %v", text, err)
	}
	if hash != index.ChunkDiffKey(text) {
		t.Fatal("snapshot hash and extraction differ")
	}
}

func TestRetrySchedulerDoesNotRepublishQuarantine(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "permanently failing revision")
	store, err := index.NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	idx := revisionIndexer(t, root, store, &revisionEmbedder{err: embed.ErrPermanent})
	ref, err := ResolveDocumentRef(root, file)
	if err != nil {
		t.Fatal(err)
	}
	version, started := idx.paths.Begin(ref.Key, nil)
	if !started {
		t.Fatal("job did not start")
	}
	defer idx.paths.Finish(ref.Key)
	_, err = idx.indexFileCoreRef(ctx, ref, nil, version)
	if !errors.Is(err, embed.ErrPermanent) {
		t.Fatalf("index error=%v", err)
	}
	// Keep the job current to ensure the scheduler cannot publish a second time,
	// even without Finish having already invalidated its revision.
	idx.scheduleIndexRetryRef(ctx, ref, time.Time{}, err)
	entries, listErr := store.ListQuarantined(ctx)
	if listErr != nil || len(entries) != 1 || entries[0].Attempts != 1 {
		t.Fatalf("expected one quarantine publication: %+v err=%v", entries, listErr)
	}
}
