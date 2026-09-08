package index

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectionAndQuarantineTransactions(t *testing.T) {
	ctx := t.Context()
	s, err := NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	doc := &Document{Path: "a.txt", Hash: "old", ModifiedAt: time.Now()}
	chunks := []ChunkRecord{{Content: "old text", Embedding: []byte{}}}
	if err := s.ReindexDocumentProjection(ctx, doc, chunks, Projection{Fingerprint: "old-config"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_chunk BEFORE INSERT ON chunks BEGIN SELECT RAISE(ABORT, 'blocked insert'); END`); err != nil {
		t.Fatal(err)
	}
	doc.Hash = "new"
	if err := s.ReindexDocumentProjection(ctx, doc, chunks, Projection{Fingerprint: "new-config"}); err == nil {
		t.Fatal("expected insert failure")
	}
	current, _ := s.GetDocumentByPath(ctx, doc.Path)
	fingerprint, _ := s.DocumentProjection(ctx, doc.Path)
	if current == nil || current.Hash != "old" || fingerprint.Fingerprint != "old-config" {
		t.Fatalf("failed replacement changed old document: %+v %q", current, fingerprint)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON documents BEGIN SELECT RAISE(ABORT, 'blocked delete'); END`); err != nil {
		t.Fatal(err)
	}
	q := QuarantineRevision{Revision: "old", Category: "extraction", Config: "config"}
	if err := s.QuarantineDocument(ctx, doc.Path, "failed", q); err == nil {
		t.Fatal("expected deletion failure")
	}
	if quarantined, _ := s.IsQuarantined(ctx, doc.Path); quarantined {
		t.Fatal("quarantine committed without deletion")
	}
	if revision, _ := s.GetQuarantineRevision(ctx, doc.Path); revision != nil {
		t.Fatal("revision committed without deletion")
	}
	if docs, count, err := s.Stats(ctx); err != nil || docs != 1 || count != 1 {
		t.Fatalf("rollback stats=%d/%d %v", docs, count, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_delete`); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineDocument(ctx, doc.Path, "failed", q); err != nil {
		t.Fatal(err)
	}
	if docs, count, err := s.Stats(ctx); err != nil || docs != 0 || count != 0 {
		t.Fatalf("quarantine stats=%d/%d %v", docs, count, err)
	}
	if fingerprint, _ := s.DocumentProjection(ctx, doc.Path); fingerprint != (Projection{}) {
		t.Fatal("orphan projection")
	}
	if err := s.RemoveFromQuarantine(ctx, doc.Path); err != nil {
		t.Fatal(err)
	}
	if revision, _ := s.GetQuarantineRevision(ctx, doc.Path); revision != nil {
		t.Fatal("explicit retry retained scoped failure")
	}
}

func TestDocumentEmbeddingCoverageIncludesDuplicateInputs(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	chunks := []ChunkRecord{
		{Content: "duplicate", ChunkIndex: 0, Embedding: []byte{}},
		{Content: "duplicate", ChunkIndex: 1, Embedding: EncodeInt8([]float32{1, 0, 0})},
	}
	if err := s.ReindexDocument(t.Context(), &Document{Path: "a.txt", ModifiedAt: time.Now()}, chunks); err != nil {
		t.Fatal(err)
	}
	complete, err := s.DocumentEmbeddingsComplete(t.Context(), "a.txt", 3)
	if err != nil || complete {
		t.Fatalf("coverage=%v err=%v", complete, err)
	}
}

func TestDeletionClearsQuarantineWithoutIndexedDocument(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	for _, path := range []string{"docs/a.txt", "docs/sub/b.txt", "docs-other/c.txt"} {
		if err := s.QuarantineDocument(ctx, path, "failed", QuarantineRevision{Revision: "hash", Category: "size", Config: "config"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteDocumentsByPrefix(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListQuarantined(ctx)
	if err != nil || len(entries) != 1 || entries[0].Path != "docs-other/c.txt" {
		t.Fatalf("quarantine after prefix removal: %+v %v", entries, err)
	}
	if err := s.DeleteDocument(ctx, "docs-other/c.txt"); err != nil {
		t.Fatal(err)
	}
	if q, err := s.GetQuarantineRevision(ctx, "docs-other/c.txt"); err != nil || q != nil {
		t.Fatalf("quarantine after removal: %+v %v", q, err)
	}
}

func TestQuarantinePreservesHealthyRuntimeIndexes(t *testing.T) {
	ctx := t.Context()
	s, err := NewStore(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 3, Normalized: true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"survivor.txt", "victim.txt"} {
		if err := s.ReindexDocument(ctx, &Document{Path: path, Hash: path, ModifiedAt: time.Now()}, []ChunkRecord{
			{Content: path, Embedding: EncodeInt8([]float32{1, 0, 0})},
		}); err != nil {
			t.Fatal(err)
		}
		if path == "survivor.txt" {
			if err := s.BuildHNSW(ctx); err != nil {
				t.Fatal(err)
			}
			// Insert the victim at level zero, leaving the survivor in every
			// upper layer. The healthy incremental-removal case is deterministic.
			s.hnsw.graph.Rng = rand.New(rand.NewSource(1))
		}
	}
	assertHealthy := func(want int) {
		t.Helper()
		if !s.HNSWReady() {
			t.Fatal("quarantine disabled HNSW")
		}
		if got := s.hnsw.Len(); got != want {
			t.Fatalf("HNSW nodes=%d, want %d", got, want)
		}
		if got := s.docEmbeds.Len(); got != want {
			t.Fatalf("document cache=%d, want %d", got, want)
		}
		generation, err := s.chunkGeneration(ctx)
		if err != nil || s.hnsw.generation.Load() != generation {
			t.Fatalf("graph generation=%d database=%d err=%v", s.hnsw.generation.Load(), generation, err)
		}
		results, err := s.Search(ctx, "survivor", []float32{1, 0, 0}, 10, "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, result := range results {
			if result.DocumentPath == "survivor.txt" {
				found = true
			}
			if want == 1 && result.DocumentPath == "victim.txt" {
				t.Fatal("quarantined document remains searchable")
			}
		}
		if !found {
			t.Fatal("unaffected document is no longer searchable")
		}
	}
	assertHealthy(2)
	q := QuarantineRevision{Revision: "failed", Category: "embedding", Config: "config"}
	if err := s.QuarantineDocument(ctx, "unindexed.txt", "failed", q); err != nil {
		t.Fatal(err)
	}
	assertHealthy(2)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_quarantine_delete BEFORE DELETE ON documents BEGIN SELECT RAISE(ABORT, 'blocked delete'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineDocument(ctx, "victim.txt", "failed", q); err == nil {
		t.Fatal("expected rollback")
	}
	assertHealthy(2)
	if quarantined, err := s.IsQuarantined(ctx, "victim.txt"); err != nil || quarantined {
		t.Fatalf("rolled-back quarantine=%v err=%v", quarantined, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_quarantine_delete`); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineDocument(ctx, "victim.txt", "failed", q); err != nil {
		t.Fatal(err)
	}
	assertHealthy(1)
	if err := s.QuarantineDocument(ctx, "another-unindexed.txt", "failed", q); err != nil {
		t.Fatal(err)
	}
	assertHealthy(1)
	// An actual graph mutation failure must not undo committed quarantine or
	// expose a broken graph to readers.
	if err := s.ReindexDocument(ctx, &Document{Path: "broken.txt", ModifiedAt: time.Now()}, []ChunkRecord{{Content: "broken", Embedding: EncodeInt8([]float32{1, 0, 0})}}); err != nil {
		t.Fatal(err)
	}
	s.hnsw.mu.Lock()
	s.hnsw.graph = nil
	s.hnsw.mu.Unlock()
	if err := s.QuarantineDocument(ctx, "broken.txt", "failed", q); err != nil {
		t.Fatal(err)
	}
	if s.HNSWReady() {
		t.Fatal("failed incremental removal left graph ready")
	}
	if doc, err := s.GetDocumentByPath(ctx, "broken.txt"); err != nil || doc != nil {
		t.Fatalf("committed quarantine lost: doc=%+v err=%v", doc, err)
	}
	results, err := s.Search(ctx, "survivor", []float32{1, 0, 0}, 10, "")
	if err != nil || len(results) != 1 || results[0].DocumentPath != "survivor.txt" {
		t.Fatalf("fallback lost unaffected document: %+v err=%v", results, err)
	}
}
