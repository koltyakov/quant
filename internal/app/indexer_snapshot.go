package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
)

// Bound disk reads even for formats whose extractor normally truncates input.
const maxIndexSnapshotBytes int64 = 100 << 20

type projectionStore interface {
	DocumentProjection(context.Context, string) (index.Projection, error)
	DocumentEmbeddingsComplete(context.Context, string, int) (bool, error)
	ReindexDocumentProjection(context.Context, *index.Document, []index.ChunkRecord, index.Projection) error
}

type scopedQuarantineStore interface {
	GetQuarantineRevision(context.Context, string) (*index.QuarantineRevision, error)
	QuarantineDocument(context.Context, string, string, index.QuarantineRevision) error
}

type revisionIndexError struct {
	error
	version  uint64
	revision string
	info     os.FileInfo
}

func (e *revisionIndexError) Unwrap() error { return e.error }

func (idx *Indexer) projectionFingerprint() string {
	return fmt.Sprintf("projection-v1:chunk=%d:overlap=%g:ocr=%s:timeout=%s:extractor=%T", idx.cfg.ChunkSize, idx.cfg.ChunkOverlap, idx.cfg.PDFOCRLang, idx.cfg.PDFOCRTimeout, idx.extractor)
}

func (idx *Indexer) embeddingSpace() string {
	if idx.embedder == nil {
		return ""
	}
	return index.ChunkDiffKey(fmt.Sprintf("provider=%s:model=%s:url=%s:dims=%d:input=%d", idx.cfg.EmbedProvider, idx.cfg.EmbedModel, idx.cfg.EmbedURL, idx.embedder.Dimensions(), index.EmbeddingInputVersion))
}

func (idx *Indexer) failureConfig(category string) string {
	if category == "embedding" {
		dims := 0
		if idx.embedder != nil {
			dims = idx.embedder.Dimensions()
		}
		return index.ChunkDiffKey(fmt.Sprintf("%s:provider=%s:model=%s:dims=%d:input=%d:url=%s:key=%s", idx.projectionFingerprint(), idx.cfg.EmbedProvider, idx.cfg.EmbedModel, dims, index.EmbeddingInputVersion, idx.cfg.EmbedURL, idx.cfg.EmbedAPIKey))
	}
	return fmt.Sprintf("%s:limit=%d", idx.projectionFingerprint(), maxIndexSnapshotBytes)
}

// snapshotSource copies and hashes exactly the same bounded bytes. Keeping the
// basename allows routers to recognize extensionless files such as Dockerfile.
func snapshotSource(ctx context.Context, path string) (snapshot, revision string, info os.FileInfo, cleanup func(), err error) {
	cleanup = func() {}
	// #nosec G304 -- callers pass a path resolved and confined to the watch root.
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil {
		return
	}
	if !info.Mode().IsRegular() {
		err = fmt.Errorf("not a regular file: %s", path)
		return
	}
	if info.Size() > maxIndexSnapshotBytes {
		revision = fmt.Sprintf("oversize:%d:%d", info.Size(), info.ModTime().UnixNano())
		err = ErrFileTooLarge
		return
	}
	dir, err := os.MkdirTemp("", "quant-snapshot-*")
	if err != nil {
		return
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	snapshot = filepath.Join(dir, filepath.Base(path))
	// #nosec G304 -- snapshot is confined to the private temporary directory.
	out, err := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(f, maxIndexSnapshotBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		err = copyErr
		return
	}
	if closeErr != nil {
		err = closeErr
		return
	}
	if n > maxIndexSnapshotBytes {
		err = ErrFileTooLarge
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	revision = fmt.Sprintf("%x", h.Sum(nil))
	return
}

func (idx *Indexer) sourceMatches(ctx context.Context, ref DocumentRef, revision string, before os.FileInfo) bool {
	info, err := statDocumentPath(idx.cfg.WatchDir, ref.AbsPath)
	if err != nil || before == nil || !os.SameFile(before, info) || before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
		return false
	}
	if info.Size() > maxIndexSnapshotBytes {
		return revision == fmt.Sprintf("oversize:%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	// #nosec G304 -- statDocumentPath validated this path against the watch root.
	f, err := os.Open(ref.AbsPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxIndexSnapshotBytes+1))
	if err != nil || n != info.Size() || ctx.Err() != nil {
		return false
	}
	after, err := statDocumentPath(idx.cfg.WatchDir, ref.AbsPath)
	return err == nil && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && revision == fmt.Sprintf("%x", h.Sum(nil))
}

func (idx *Indexer) quarantineRevisionFailure(ctx context.Context, ref DocumentRef, version uint64, revision string, info os.FileInfo, failure error) {
	store, ok := idx.quarantine.(scopedQuarantineStore)
	if !ok {
		return
	}
	category := "extraction"
	if errors.Is(failure, embed.ErrPermanent) {
		category = "embedding"
	}
	if errors.Is(failure, ErrFileTooLarge) {
		category = "size"
	}
	_, err := idx.paths.Publish(ref.Key, version, func() error {
		if !idx.sourceMatches(ctx, ref, revision, info) {
			return nil
		}
		return store.QuarantineDocument(ctx, ref.Key, failure.Error(), index.QuarantineRevision{
			Revision: revision, Category: category, Config: idx.failureConfig(category),
		})
	})
	if err != nil {
		logx.Warn("quarantining revision failed", "path", ref.Key, "err", err)
	}
	idx.invalidateQuarantineCache()
}

func (idx *Indexer) removeCurrentDocument(ctx context.Context, ref DocumentRef, doc *index.Document, version uint64) (IndexAction, error) {
	action := IndexNoop
	_, err := idx.paths.Publish(ref.Key, version, func() error {
		var err error
		action, err = removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
		return err
	})
	return action, err
}
