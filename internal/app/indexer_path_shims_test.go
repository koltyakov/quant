package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
)

// Path-based shims over the DocumentRef API.
//
// Production code resolves a path to a DocumentRef once and passes the ref
// down, so these wrappers have no non-test callers. They live here rather than
// in indexer.go so the shipped binary carries only the ref-based entry points,
// while tests keep addressing documents by plain filesystem path.

func NormalizeStoredDocumentPath(root, storedPath string) (string, error) {
	return DocumentKey(root, filepath.Join(root, storedDocumentPath(storedPath)))
}

func (idx *Indexer) SyncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (IndexAction, error) {
	if path != "" {
		return idx.SyncDocumentRef(ctx, DocumentRef{Key: key, AbsPath: path}, modTime, doc)
	}
	ref, err := ResolveDocumentRefFromKey(idx.cfg.WatchDir, key)
	if err != nil {
		return IndexNoop, fmt.Errorf("resolving document key: %w", err)
	}
	return idx.SyncDocumentRef(ctx, ref, modTime, doc)
}

func (idx *Indexer) syncDocumentOnce(ctx context.Context, key, path string, doc *index.Document, version uint64) (IndexAction, error) {
	if path != "" {
		return idx.syncDocumentOnceRef(ctx, DocumentRef{Key: key, AbsPath: path}, doc, version)
	}
	ref, err := ResolveDocumentRefFromKey(idx.cfg.WatchDir, key)
	if err != nil {
		return IndexNoop, fmt.Errorf("resolving document key: %w", err)
	}
	return idx.syncDocumentOnceRef(ctx, ref, doc, version)
}

func (idx *Indexer) IndexFile(ctx context.Context, path string, modTime time.Time) (IndexAction, error) {
	ref, err := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if err != nil {
		return IndexNoop, fmt.Errorf("computing document key: %w", err)
	}
	return idx.IndexDocument(ctx, ref, modTime)
}

func (idx *Indexer) indexFileCore(ctx context.Context, key, path string, modTime time.Time, precomputedHash string, doc *index.Document, version uint64) (IndexAction, error) {
	var fileSize int64 = -1
	if info, err := os.Stat(path); err == nil {
		fileSize = info.Size()
	}
	if path != "" {
		return idx.indexFileCoreRef(ctx, DocumentRef{Key: key, AbsPath: path}, modTime, fileSize, precomputedHash, doc, version)
	}
	ref, err := ResolveDocumentRefFromKey(idx.cfg.WatchDir, key)
	if err != nil {
		return IndexNoop, fmt.Errorf("resolving document key: %w", err)
	}
	return idx.indexFileCoreRef(ctx, ref, modTime, fileSize, precomputedHash, doc, version)
}

func (idx *Indexer) processLiveIndexRequest(ctx context.Context, path string) {
	ref, err := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if err != nil {
		logx.Error("computing document key failed", "path", path, "err", err)
		return
	}
	idx.processLiveIndexRequestKey(ctx, ref.Key)
}

func (idx *Indexer) processLiveIndexRequestDirect(ctx context.Context, path string, modTime time.Time) {
	ref, err := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if err != nil {
		logx.Error("computing document key failed", "path", path, "err", err)
		return
	}
	idx.processLiveIndexDocumentDirect(ctx, ref, modTime)
}

func (idx *Indexer) scheduleIndexRetry(ctx context.Context, path string, modTime time.Time, err error) {
	ref, refErr := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if refErr != nil {
		logx.Warn("computing retry document key failed", "path", path, "err", refErr)
		return
	}
	idx.scheduleIndexRetryRef(ctx, ref, modTime, err)
}

func (idx *Indexer) quarantineFailedPath(ctx context.Context, path string, failure error) {
	if idx == nil || idx.cfg == nil || path == "" || failure == nil {
		return
	}
	ref, refErr := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if refErr != nil {
		logx.Warn("computing key for quarantine failed", "path", path, "err", refErr)
		return
	}
	idx.quarantineFailedRef(ctx, ref, failure)
}
