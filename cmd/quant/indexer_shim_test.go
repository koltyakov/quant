package main

import (
	"context"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/app"
	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/watch"
)

var (
	indexRetryBaseDelay   = app.IndexRetryBaseDelay
	maxIndexRetryAttempts = app.MaxIndexRetryAttempts
)

type indexAction = app.IndexAction

const (
	indexNoop    = app.IndexNoop
	indexUpdated = app.IndexUpdated
	indexRemoved = app.IndexRemoved
)

type ResyncCoordinator = app.ResyncCoordinator
type ResyncCallbacks = app.ResyncCallbacks

type syncReport struct {
	hadIndexFailures bool
}

func newSyncReport(report app.SyncReport) syncReport {
	return syncReport{hadIndexFailures: report.HadIndexFailures}
}

func syncRetrySettings() {
	app.IndexRetryBaseDelay = indexRetryBaseDelay
	app.MaxIndexRetryAttempts = maxIndexRetryAttempts
}

type indexer struct {
	cfg       *config.Config
	store     index.DocumentWriter
	hnswStore index.HNSWBuilder
	embedder  embed.Embedder
	extractor extract.Extractor

	indexState *runtimestate.IndexStateTracker

	// inner holds the real app.Indexer once built.
	inner *app.Indexer
}

func (idx *indexer) ensureInner() *app.Indexer {
	if idx.inner == nil {
		syncRetrySettings()
		idx.inner = app.NewIndexer(app.IndexerConfig{
			Cfg:       idx.cfg,
			Store:     idx.store,
			HNSWStore: idx.hnswStore,
			Embedder:  idx.embedder,
			Extractor: idx.extractor,
		})
		if idx.indexState != nil {
			idx.inner.IndexState = idx.indexState
		}
	}
	return idx.inner
}

func (idx *indexer) initialSync(ctx context.Context) error {
	return idx.ensureInner().InitialSync(ctx)
}

func (idx *indexer) initialSyncWithReport(ctx context.Context) (syncReport, error) {
	report, err := idx.ensureInner().InitialSyncWithReport(ctx)
	return newSyncReport(report), err
}

func (idx *indexer) startLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	idx.ensureInner().StartLiveIndexWorkers(ctx, wg)
}

func (idx *indexer) enqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	return idx.ensureInner().EnqueueLiveIndex(ctx, path, modTime)
}

func (idx *indexer) handleWatchEvent(ctx context.Context, event watch.Event) {
	idx.ensureInner().HandleWatchEvent(ctx, event)
}

func (idx *indexer) syncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (indexAction, error) {
	return idx.ensureInner().SyncDocument(ctx, key, path, modTime, doc)
}

func (idx *indexer) indexFile(ctx context.Context, path string, modTime time.Time) (indexAction, error) {
	return idx.ensureInner().IndexFile(ctx, path, modTime)
}

func documentKey(root, path string) (string, error) {
	return app.DocumentKey(root, path)
}
