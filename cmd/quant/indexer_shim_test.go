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
	"github.com/koltyakov/quant/internal/ingest"
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

type pathSyncTracker struct {
	inner *app.PathSyncTracker
}

func newPathSyncTracker() *pathSyncTracker {
	return &pathSyncTracker{inner: app.NewPathSyncTracker()}
}

type liveIndexQueue struct {
	inner *app.LiveIndexQueue
	jobs  chan string
}

func newLiveIndexQueue(queueSize int) *liveIndexQueue {
	inner := app.NewLiveIndexQueue(queueSize)
	return &liveIndexQueue{inner: inner, jobs: inner.Jobs}
}

func wrapLiveIndexQueue(inner *app.LiveIndexQueue) *liveIndexQueue {
	if inner == nil {
		return nil
	}
	return &liveIndexQueue{inner: inner, jobs: inner.Jobs}
}

func (q *liveIndexQueue) startProcessing(path string) (time.Time, bool) {
	return q.inner.StartProcessing(path)
}

type retryScheduler struct {
	inner *app.RetryScheduler
}

func newRetryScheduler() *retryScheduler {
	syncRetrySettings()
	return &retryScheduler{inner: app.NewRetryScheduler()}
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
	pipeline  *ingest.Pipeline

	paths   *pathSyncTracker
	live    *liveIndexQueue
	retries *retryScheduler

	indexState *runtimestate.IndexStateTracker
	resync     *ResyncCoordinator
}

func (idx *indexer) toApp() *app.Indexer {
	syncRetrySettings()
	if idx.paths == nil {
		idx.paths = newPathSyncTracker()
	}
	return &app.Indexer{
		Cfg:        idx.cfg,
		Store:      idx.store,
		HNSWStore:  idx.hnswStore,
		Embedder:   idx.embedder,
		Extractor:  idx.extractor,
		Pipeline:   idx.pipeline,
		Paths:      idx.paths.inner,
		IndexState: idx.indexState,
		Resync:     idx.resync,
		Live:       unwrapLiveIndexQueue(idx.live),
		Retries:    unwrapRetryScheduler(idx.retries),
	}
}

func (idx *indexer) fromApp(inner *app.Indexer) {
	if inner == nil {
		return
	}
	idx.pipeline = inner.Pipeline
	idx.resync = inner.Resync
	if inner.Live != nil {
		idx.live = wrapLiveIndexQueue(inner.Live)
	}
	if inner.Retries != nil && idx.retries == nil {
		idx.retries = &retryScheduler{inner: inner.Retries}
	}
	if inner.Paths != nil && idx.paths == nil {
		idx.paths = &pathSyncTracker{inner: inner.Paths}
	}
}

func unwrapLiveIndexQueue(q *liveIndexQueue) *app.LiveIndexQueue {
	if q == nil {
		return nil
	}
	return q.inner
}

func unwrapRetryScheduler(r *retryScheduler) *app.RetryScheduler {
	if r == nil {
		return nil
	}
	return r.inner
}

func (idx *indexer) initialSync(ctx context.Context) error {
	inner := idx.toApp()
	err := inner.InitialSync(ctx)
	idx.fromApp(inner)
	return err
}

func (idx *indexer) initialSyncWithReport(ctx context.Context) (syncReport, error) {
	inner := idx.toApp()
	report, err := inner.InitialSyncWithReport(ctx)
	idx.fromApp(inner)
	return newSyncReport(report), err
}

func (idx *indexer) startLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	inner := idx.toApp()
	inner.StartLiveIndexWorkers(ctx, wg)
	idx.fromApp(inner)
}

func (idx *indexer) enqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	inner := idx.toApp()
	ok := inner.EnqueueLiveIndex(ctx, path, modTime)
	idx.fromApp(inner)
	return ok
}

func (idx *indexer) handleWatchEvent(ctx context.Context, event watch.Event) {
	inner := idx.toApp()
	inner.HandleWatchEvent(ctx, event)
	idx.fromApp(inner)
}

func (idx *indexer) syncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (indexAction, error) {
	inner := idx.toApp()
	action, err := inner.SyncDocument(ctx, key, path, modTime, doc)
	idx.fromApp(inner)
	return action, err
}

func (idx *indexer) indexFile(ctx context.Context, path string, modTime time.Time) (indexAction, error) {
	inner := idx.toApp()
	action, err := inner.IndexFile(ctx, path, modTime)
	idx.fromApp(inner)
	return action, err
}

func documentKey(root, path string) (string, error) {
	return app.DocumentKey(root, path)
}
