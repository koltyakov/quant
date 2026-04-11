package index

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)
}

func TestNewStore_CreatesParentDirectory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), ".index", "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected database file to exist: %v", err)
	}
}

func TestStore_UpsertAndGetDocument(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc := &Document{
		Path:       "/test/file.txt",
		Hash:       "abc123",
		ModifiedAt: time.Now(),
	}

	id, err := store.UpsertDocument(ctx, doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := store.GetDocumentByPath(ctx, "/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected document, got nil")
	}
	if got.Path != doc.Path {
		t.Errorf("expected path %q, got %q", doc.Path, got.Path)
	}
	if got.Hash != doc.Hash {
		t.Errorf("expected hash %q, got %q", doc.Hash, got.Hash)
	}
}

func TestStore_UpsertDocument_Update(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc := &Document{
		Path:       "/test/file.txt",
		Hash:       "abc123",
		ModifiedAt: time.Now(),
	}

	id1, _ := store.UpsertDocument(ctx, doc)
	doc.Hash = "def456"
	id2, _ := store.UpsertDocument(ctx, doc)

	if id1 != id2 {
		t.Errorf("expected same id on update, got %d and %d", id1, id2)
	}

	got, _ := store.GetDocumentByPath(ctx, "/test/file.txt")
	if got.Hash != "def456" {
		t.Errorf("expected updated hash, got %q", got.Hash)
	}
}

func TestStore_InsertAndSearchChunks(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc := &Document{
		Path:       "/test/file.txt",
		Hash:       "abc123",
		ModifiedAt: time.Now(),
	}
	docID, _ := store.UpsertDocument(ctx, doc)

	embedding := make([]float32, 8)
	embedding[0] = 1.0
	chunk1 := &ChunkRecord{
		DocumentID: docID,
		Content:    "hello world",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(embedding),
	}
	if err := store.InsertChunk(ctx, chunk1); err != nil {
		t.Fatalf("unexpected error inserting chunk1: %v", err)
	}

	embedding2 := make([]float32, 8)
	embedding2[1] = 1.0
	chunk2 := &ChunkRecord{
		DocumentID: docID,
		Content:    "goodbye world",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32(embedding2),
	}
	if err := store.InsertChunk(ctx, chunk2); err != nil {
		t.Fatalf("unexpected error inserting chunk2: %v", err)
	}

	query := make([]float32, 8)
	query[0] = 1.0
	query = NormalizeFloat32(query)

	results, err := store.Search(ctx, "hello goodbye", query, 5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ChunkContent != "hello world" {
		t.Errorf("expected first result 'hello world', got %q", results[0].ChunkContent)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", results[0].Score)
	}
}

func TestStore_SearchWithPathPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	// Create two documents in different paths.
	doc1 := &Document{Path: "/src/main.go", Hash: "a1", ModifiedAt: time.Now()}
	doc2 := &Document{Path: "/docs/readme.md", Hash: "b2", ModifiedAt: time.Now()}

	id1, _ := store.UpsertDocument(ctx, doc1)
	id2, _ := store.UpsertDocument(ctx, doc2)

	embedding := make([]float32, 8)
	embedding[0] = 1.0
	if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: id1, Content: "hello from source code", ChunkIndex: 0, Embedding: EncodeFloat32(embedding)}); err != nil {
		t.Fatalf("unexpected error inserting first chunk: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: id2, Content: "hello from documentation", ChunkIndex: 0, Embedding: EncodeFloat32(embedding)}); err != nil {
		t.Fatalf("unexpected error inserting second chunk: %v", err)
	}

	query := NormalizeFloat32(embedding)

	// Search all.
	all, err := store.Search(ctx, "hello", query, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 results, got %d", len(all))
	}

	// Search with path prefix.
	srcOnly, err := store.Search(ctx, "hello", query, 10, "/src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcOnly) != 1 {
		t.Fatalf("expected 1 result for /src prefix, got %d", len(srcOnly))
	}
	if srcOnly[0].DocumentPath != "/src/main.go" {
		t.Errorf("expected /src/main.go, got %s", srcOnly[0].DocumentPath)
	}
}

func TestStore_SearchWithPathPrefix_TreatsWildcardsLiterally(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc1 := &Document{Path: "docs_/match.md", Hash: "a1", ModifiedAt: time.Now()}
	doc2 := &Document{Path: "docsa/other.md", Hash: "b2", ModifiedAt: time.Now()}

	id1, err := store.UpsertDocument(ctx, doc1)
	if err != nil {
		t.Fatalf("unexpected upsert error: %v", err)
	}
	id2, err := store.UpsertDocument(ctx, doc2)
	if err != nil {
		t.Fatalf("unexpected upsert error: %v", err)
	}

	embedding := NormalizeFloat32([]float32{1})
	for _, chunk := range []struct {
		docID   int64
		content string
	}{
		{docID: id1, content: "hello from literal underscore path"},
		{docID: id2, content: "hello from wildcard lookalike path"},
	} {
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: chunk.docID,
			Content:    chunk.content,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(embedding),
		}); err != nil {
			t.Fatalf("unexpected insert error: %v", err)
		}
	}

	results, err := store.Search(ctx, "hello", embedding, 10, "docs_")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 literal-prefix result, got %d", len(results))
	}
	if results[0].DocumentPath != "docs_/match.md" {
		t.Fatalf("expected docs_/match.md, got %s", results[0].DocumentPath)
	}
}

func TestStore_SearchVectorFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc := &Document{Path: "/test/file.txt", Hash: "abc", ModifiedAt: time.Now()}
	docID, _ := store.UpsertDocument(ctx, doc)

	embedding := make([]float32, 8)
	embedding[0] = 1.0
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "hello world",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(embedding),
	}); err != nil {
		t.Fatalf("unexpected insert error: %v", err)
	}

	query := NormalizeFloat32(embedding)

	// Use a query string that won't match FTS but should still return via vector fallback.
	results, err := store.Search(ctx, "xyznonexistent", query, 5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from vector fallback, got %d", len(results))
	}
}

func TestStore_Search_MergesANDAndORCandidates(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc1 := &Document{Path: "/test/and.txt", Hash: "and", ModifiedAt: time.Now()}
	doc2 := &Document{Path: "/test/or.txt", Hash: "or", ModifiedAt: time.Now()}

	doc1ID, err := store.UpsertDocument(ctx, doc1)
	if err != nil {
		t.Fatalf("unexpected upsert error: %v", err)
	}
	doc2ID, err := store.UpsertDocument(ctx, doc2)
	if err != nil {
		t.Fatalf("unexpected upsert error: %v", err)
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: doc1ID,
		Content:    "alpha beta",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{0, 1})),
	}); err != nil {
		t.Fatalf("unexpected insert error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: doc2ID,
		Content:    "alpha",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}); err != nil {
		t.Fatalf("unexpected insert error: %v", err)
	}

	results, err := store.Search(ctx, "alpha beta", NormalizeFloat32([]float32{1, 0}), 2, "")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected merged AND/OR search to return 2 results, got %d", len(results))
	}
	if results[0].ChunkContent != "alpha beta" {
		t.Fatalf("expected precise AND match first, got %q", results[0].ChunkContent)
	}
	if results[1].ChunkContent != "alpha" {
		t.Fatalf("expected OR candidate to fill remaining slot, got %q", results[1].ChunkContent)
	}
}

func TestStore_SearchVectorFallback_ScansPastInitialWindow(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	for i := 0; i < 250; i++ {
		doc := &Document{
			Path:       "/test/file" + strconv.Itoa(i) + ".txt",
			Hash:       "hash-" + strconv.Itoa(i),
			ModifiedAt: time.Now(),
		}
		docID, err := store.UpsertDocument(ctx, doc)
		if err != nil {
			t.Fatalf("unexpected upsert error: %v", err)
		}

		embedding := []float32{0}
		content := "noise chunk"
		if i == 249 {
			embedding = []float32{1}
			content = "best chunk"
		}

		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: docID,
			Content:    content,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(embedding),
		}); err != nil {
			t.Fatalf("unexpected insert error: %v", err)
		}
	}

	results, err := store.Search(ctx, "xyznonexistent", NormalizeFloat32([]float32{1}), 1, "")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ChunkContent != "best chunk" {
		t.Fatalf("expected best chunk from full vector scan, got %q", results[0].ChunkContent)
	}
}

func TestStore_SearchVectorFallback_SkipsLargeCorpusWhenCapped(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store.SetMaxVectorSearchCandidates(3)
	mustCloseStore(t, store)

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		doc := &Document{
			Path:       "/test/file" + strconv.Itoa(i) + ".txt",
			Hash:       "hash-" + strconv.Itoa(i),
			ModifiedAt: time.Now(),
		}
		docID, err := store.UpsertDocument(ctx, doc)
		if err != nil {
			t.Fatalf("unexpected upsert error: %v", err)
		}
		embedding := []float32{0}
		if i == 4 {
			embedding = []float32{1}
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: docID,
			Content:    "chunk " + strconv.Itoa(i),
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(embedding),
		}); err != nil {
			t.Fatalf("unexpected insert error: %v", err)
		}
	}

	results, err := store.Search(ctx, "xyznonexistent", NormalizeFloat32([]float32{1}), 1, "")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected capped vector fallback to skip results, got %+v", results)
	}
}

func TestStore_SearchVectorFallback_RunsWithinConfiguredCap(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store.SetMaxVectorSearchCandidates(5)
	mustCloseStore(t, store)

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		doc := &Document{
			Path:       "/test/within-cap-" + strconv.Itoa(i) + ".txt",
			Hash:       "hash-" + strconv.Itoa(i),
			ModifiedAt: time.Now(),
		}
		docID, err := store.UpsertDocument(ctx, doc)
		if err != nil {
			t.Fatalf("unexpected upsert error: %v", err)
		}
		embedding := []float32{0}
		content := "noise"
		if i == 4 {
			embedding = []float32{1}
			content = "best chunk"
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: docID,
			Content:    content,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(embedding),
		}); err != nil {
			t.Fatalf("unexpected insert error: %v", err)
		}
	}

	results, err := store.Search(ctx, "xyznonexistent", NormalizeFloat32([]float32{1}), 1, "")
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if len(results) != 1 || results[0].ChunkContent != "best chunk" {
		t.Fatalf("expected vector fallback within cap to find best chunk, got %+v", results)
	}
}

func TestStore_DeleteDocument(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc := &Document{
		Path:       "/test/file.txt",
		Hash:       "abc123",
		ModifiedAt: time.Now(),
	}
	docID, _ := store.UpsertDocument(ctx, doc)
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "hello world",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("unexpected error inserting chunk: %v", err)
	}

	err = store.DeleteDocument(ctx, "/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := store.GetDocumentByPath(ctx, "/test/file.txt")
	if got != nil {
		t.Error("expected nil after deletion")
	}

	_, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("unexpected error reading stats: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("expected chunk cascade delete, got %d chunks", chunkCount)
	}
}

func TestStore_DeleteDocumentsByPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for _, path := range []string{"docs/readme.md", "docs/nested/guide.md", "src/main.go"} {
		docID, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       "hash-" + path,
			ModifiedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("unexpected upsert error for %s: %v", path, err)
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: docID,
			Content:    "chunk for " + path,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("unexpected insert error for %s: %v", path, err)
		}
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "docs"); err != nil {
		t.Fatalf("unexpected delete-by-prefix error: %v", err)
	}

	docs, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 remaining document, got %d", len(docs))
	}
	if docs[0].Path != "src/main.go" {
		t.Fatalf("expected src/main.go to remain, got %s", docs[0].Path)
	}
}

func TestStore_Stats(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Errorf("expected empty stats, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestStore_ListDocuments(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		doc := &Document{
			Path:       "/test/file" + string(rune('A'+i)) + ".txt",
			Hash:       "hash",
			ModifiedAt: time.Now(),
		}
		if _, err := store.UpsertDocument(ctx, doc); err != nil {
			t.Fatalf("unexpected upsert error: %v", err)
		}
	}

	docs, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("expected 3 documents, got %d", len(docs))
	}
}

func TestEncodeDecodeFloat32(t *testing.T) {
	original := []float32{1.0, -2.5, 0.0, 3.14, -100.0}
	encoded := EncodeFloat32(original)
	decoded := decodeFloat32(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("expected %d elements, got %d", len(original), len(decoded))
	}
	for i, v := range original {
		if decoded[i] != v {
			t.Errorf("element %d: expected %f, got %f", i, v, decoded[i])
		}
	}
}

func TestNormalizeFloat32(t *testing.T) {
	vec := NormalizeFloat32([]float32{3, 4})
	if len(vec) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(vec))
	}
	if vec[0] != 0.6 || vec[1] != 0.8 {
		t.Fatalf("expected normalized vector [0.6 0.8], got %v", vec)
	}
}

func TestDotProduct(t *testing.T) {
	a := NormalizeFloat32([]float32{1, 0, 0})
	b := NormalizeFloat32([]float32{1, 0, 0})
	score := dotProduct(a, b)
	if score != 1.0 {
		t.Errorf("expected dot product 1.0, got %f", score)
	}

	c := NormalizeFloat32([]float32{0, 1, 0})
	score = dotProduct(a, c)
	if score != 0.0 {
		t.Errorf("expected dot product 0.0, got %f", score)
	}
}

func TestDotProductEncoded(t *testing.T) {
	query := NormalizeFloat32([]float32{3, 4})
	encoded := EncodeFloat32([]float32{0.6, 0.8})

	score := dotProductEncoded(query, encoded)
	if score < 0.999 || score > 1.001 {
		t.Fatalf("expected score near 1.0, got %f", score)
	}
}

func TestStore_SearchResultScoreKinds(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc := &Document{Path: "/test/file.txt", Hash: "abc", ModifiedAt: time.Now()}
	docID, _ := store.UpsertDocument(ctx, doc)

	embedding := NormalizeFloat32([]float32{1, 0})
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "hello world",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(embedding),
	}); err != nil {
		t.Fatalf("unexpected insert error: %v", err)
	}

	hybrid, err := store.Search(ctx, "hello", embedding, 1, "")
	if err != nil {
		t.Fatalf("unexpected hybrid search error: %v", err)
	}
	if len(hybrid) != 1 || hybrid[0].ScoreKind != "rrf" {
		t.Fatalf("expected hybrid result with rrf score kind, got %+v", hybrid)
	}

	vectorOnly, err := store.Search(ctx, "noftsresult", embedding, 1, "")
	if err != nil {
		t.Fatalf("unexpected vector search error: %v", err)
	}
	if len(vectorOnly) != 1 || vectorOnly[0].ScoreKind != "cosine" {
		t.Fatalf("expected vector result with cosine score kind, got %+v", vectorOnly)
	}
}

func TestStore_ReindexDocument_RollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	original := &Document{
		Path:       "/test/file.txt",
		Hash:       "old-hash",
		ModifiedAt: time.Now(),
	}
	if err := store.ReindexDocument(ctx, original, []ChunkRecord{{
		Content:    "old chunk",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}}); err != nil {
		t.Fatalf("unexpected error seeding document: %v", err)
	}

	updated := &Document{
		Path:       "/test/file.txt",
		Hash:       "new-hash",
		ModifiedAt: time.Now(),
	}
	err = store.ReindexDocument(ctx, updated, []ChunkRecord{
		{Content: "first new chunk", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1})},
		{Content: "duplicate index chunk", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1})},
	})
	if err == nil {
		t.Fatal("expected transactional reindex to fail")
	}

	doc, err := store.GetDocumentByPath(ctx, "/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error loading document: %v", err)
	}
	if doc == nil || doc.Hash != "old-hash" {
		t.Fatalf("expected original document hash to remain, got %+v", doc)
	}

	results, err := store.Search(ctx, "old", NormalizeFloat32([]float32{1}), 5, "")
	if err != nil {
		t.Fatalf("unexpected error searching after rollback: %v", err)
	}
	if len(results) != 1 || results[0].ChunkContent != "old chunk" {
		t.Fatalf("expected original chunk to remain after rollback, got %+v", results)
	}
}

func TestStore_EnsureEmbeddingMetadata_ResetOnChange(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "/test/file.txt",
		Hash:       "abc123",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error creating document: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "hello world",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("unexpected error creating chunk: %v", err)
	}

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{
		Model:      "model-a",
		Dimensions: 1,
		Normalized: true,
	})
	if err != nil {
		t.Fatalf("unexpected metadata error: %v", err)
	}
	if !rebuild {
		t.Fatal("expected metadata bootstrap on populated index to trigger rebuild")
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("unexpected error reading stats: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected rebuilt index to be empty, got %d docs and %d chunks", docCount, chunkCount)
	}
}

func TestBuildFTSQueries_Basic(t *testing.T) {
	tests := []struct {
		input   string
		wantAND string
		wantOR  string
	}{
		{"hello world", "hello AND world", "hello OR world"},
		{"", "", ""},
		{"a b c", "a AND b AND c", "a OR b OR c"},
		{"hello hello", "hello", "hello"}, // single token, AND == OR
		{"single", "single", "single"},
	}

	for _, tt := range tests {
		gotAND, gotOR := buildFTSQueries(tt.input)
		if gotAND != tt.wantAND {
			t.Errorf("buildFTSQueries(%q) AND = %q, want %q", tt.input, gotAND, tt.wantAND)
		}
		if gotOR != tt.wantOR {
			t.Errorf("buildFTSQueries(%q) OR = %q, want %q", tt.input, gotOR, tt.wantOR)
		}
	}
}

func TestBuildFTSQueries_Phrases(t *testing.T) {
	gotAND, gotOR := buildFTSQueries(`"exact match" other`)
	if gotAND != `"exact match" AND other` {
		t.Errorf("unexpected AND query with phrase: %q", gotAND)
	}
	if gotOR != `"exact match" OR other` {
		t.Errorf("unexpected OR query with phrase: %q", gotOR)
	}
}

func mustCloseStore(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})
}
