package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/testutil"
	"github.com/koltyakov/quant/internal/watch"
)

// Notify the editor at four points during the warm scan, just before the
// workers hash those files. The buffered channel never stalls a scan worker.
type warmScanStore struct {
	*index.Store
	edits chan struct{}
}

func (s *warmScanStore) DocumentEmbeddingsComplete(ctx context.Context, path string, dims int) (bool, error) {
	complete, err := s.Store.DocumentEmbeddingsComplete(ctx, path, dims)
	switch path {
	case "warm-008.txt", "warm-024.txt", "warm-040.txt", "warm-056.txt":
		s.edits <- struct{}{}
	}
	return complete, err
}

func BenchmarkWarmRescanWithEdits(b *testing.B) {
	for _, size := range []int{16 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			cfg := config.Default()
			cfg.WatchDir = b.TempDir()
			cfg.DBPath = filepath.Join(b.TempDir(), "index.db")
			cfg.IndexWorkers = 4
			store, err := index.NewStore(cfg.DBPath)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = store.Close() })
			idx := NewIndexer(IndexerConfig{
				Cfg: cfg, Store: store, Quarantine: store, DedupStore: store,
				Extractor: extract.NewRouter(), Embedder: testutil.StaticEmbedder{},
			})
			// Padding keeps each file at one chunk, varying the cost of source
			// verification independently of embedding and database row counts.
			content := []byte("document" + strings.Repeat(" ", size-len("document")))
			for i := range 64 {
				path := filepath.Join(cfg.WatchDir, fmt.Sprintf("warm-%03d.txt", i))
				if err := os.WriteFile(path, content, 0o600); err != nil {
					b.Fatal(err)
				}
			}
			const hotKey = "000-edit.txt"
			hotPath := filepath.Join(cfg.WatchDir, hotKey)
			if err := os.WriteFile(hotPath, []byte("initial revision"), 0o600); err != nil {
				b.Fatal(err)
			}
			ctx := b.Context()
			if report, err := idx.InitialSyncWithReport(ctx); err != nil || report.HadIndexFailures {
				b.Fatalf("seed scan: %+v, %v", report, err)
			}
			observed := &warmScanStore{Store: store}
			idx.store = observed
			var enqueueTime, publishTime time.Duration
			revision := 0
			for b.Loop() {
				observed.edits = make(chan struct{}, 4)
				scanned := make(chan error, 1)
				go func() {
					report, err := idx.InitialSyncWithReport(ctx)
					if err == nil && report.HadIndexFailures {
						err = fmt.Errorf("warm scan reported indexing failures")
					}
					scanned <- err
					close(observed.edits)
				}()
				count := 0
				for range observed.edits {
					revision++
					count++
					text := fmt.Sprintf("revision %d", revision)
					if err := os.WriteFile(hotPath, []byte(text), 0o600); err != nil {
						b.Fatal(err)
					}
					start := time.Now()
					idx.HandleWatchEvent(ctx, watch.Event{Path: hotPath, Op: watch.Write})
					enqueueTime += time.Since(start)
					select {
					case key := <-idx.live.Jobs:
						idx.processLiveIndexRequestKey(ctx, key)
					default:
						b.Fatal("edit was not queued")
					}
					publishTime += time.Since(start)
					doc, err := store.GetDocumentByPath(ctx, hotKey)
					if err != nil || doc == nil || doc.Hash != index.ChunkDiffKey(text) {
						b.Fatalf("edit %d was not published: %+v, %v", revision, doc, err)
					}
				}
				if err := <-scanned; err != nil || count != 4 {
					b.Fatalf("warm scan: edits=%d, err=%v", count, err)
				}
			}
			b.ReportMetric(float64(enqueueTime.Microseconds())/float64(revision), "us/enqueue")
			b.ReportMetric(float64(publishTime.Microseconds())/float64(revision), "us/publish")
		})
	}
}
