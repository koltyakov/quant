package chunk

import (
	"testing"
)

func TestSemanticChunker_Name(t *testing.T) {
	t.Parallel()
	s := &SemanticChunker{}
	if got := s.Name(); got != "semantic" {
		t.Errorf("Name() = %q, want %q", got, "semantic")
	}
}

func TestSemanticChunker_Supports(t *testing.T) {
	t.Parallel()
	s := &SemanticChunker{}
	for _, path := range []string{"test.txt", "main.go", "app.py", "", "README.md"} {
		if s.Supports(path) {
			t.Errorf("Supports(%q) = true, want false", path)
		}
	}
}

func TestSemanticChunker_Priority(t *testing.T) {
	t.Parallel()
	s := &SemanticChunker{}
	if got := s.Priority(); got != -10 {
		t.Errorf("Priority() = %d, want -10", got)
	}
}

func TestSemanticChunker_Split(t *testing.T) {
	t.Parallel()
	s := &SemanticChunker{}
	text := "hello world this is a test of semantic splitting"
	chunks := s.Split(text, 5, 0.2)
	expected := Split(text, 5, 0.2)
	if len(chunks) != len(expected) {
		t.Fatalf("Split() returned %d chunks, want %d", len(chunks), len(expected))
	}
	for i := range chunks {
		if chunks[i].Content != expected[i].Content {
			t.Errorf("chunk %d: Content = %q, want %q", i, chunks[i].Content, expected[i].Content)
		}
	}
}

func TestSemanticChunker_Split_Empty(t *testing.T) {
	t.Parallel()
	s := &SemanticChunker{}
	chunks := s.Split("", 512, 0.15)
	if chunks != nil {
		t.Errorf("Split empty = %v, want nil", chunks)
	}
}

func TestFindSemanticBoundaries_ZeroOrOneParagraphs(t *testing.T) {
	t.Parallel()
	sim := func(a, b string) float64 { return 1.0 }

	if got := FindSemanticBoundaries(nil, sim, 0.5); got != nil {
		t.Errorf("FindSemanticBoundaries(nil) = %v, want nil", got)
	}

	if got := FindSemanticBoundaries([]string{}, sim, 0.5); got != nil {
		t.Errorf("FindSemanticBoundaries([]) = %v, want nil", got)
	}

	if got := FindSemanticBoundaries([]string{"only one"}, sim, 0.5); got != nil {
		t.Errorf("FindSemanticBoundaries(single) = %v, want nil", got)
	}
}

func TestFindSemanticBoundaries_BelowThreshold(t *testing.T) {
	t.Parallel()
	paragraphs := []string{
		"first paragraph text",
		"second paragraph text",
		"third paragraph text",
	}
	callCount := 0
	sim := func(a, b string) float64 {
		callCount++
		return 0.2
	}

	boundaries := FindSemanticBoundaries(paragraphs, sim, 0.5)
	if len(boundaries) != 2 {
		t.Fatalf("expected 2 boundaries, got %d", len(boundaries))
	}

	pos0 := len(paragraphs[0]) + 1
	if boundaries[0].Position != pos0 {
		t.Errorf("boundary 0 Position = %d, want %d", boundaries[0].Position, pos0)
	}
	if boundaries[0].Score != 0.2 {
		t.Errorf("boundary 0 Score = %f, want 0.2", boundaries[0].Score)
	}

	pos1 := len(paragraphs[0]) + 1 + len(paragraphs[1]) + 1
	if boundaries[1].Position != pos1 {
		t.Errorf("boundary 1 Position = %d, want %d", boundaries[1].Position, pos1)
	}
	if boundaries[1].Score != 0.2 {
		t.Errorf("boundary 1 Score = %f, want 0.2", boundaries[1].Score)
	}
}

func TestFindSemanticBoundaries_MixedThreshold(t *testing.T) {
	t.Parallel()
	paragraphs := []string{"aaa", "bbb", "ccc", "ddd"}
	sim := func(a, b string) float64 {
		if a == "aaa" && b == "bbb" {
			return 0.9
		}
		if a == "bbb" && b == "ccc" {
			return 0.3
		}
		return 0.8
	}

	boundaries := FindSemanticBoundaries(paragraphs, sim, 0.5)
	if len(boundaries) != 1 {
		t.Fatalf("expected 1 boundary, got %d", len(boundaries))
	}
	if boundaries[0].Position != len("aaa")+1+len("bbb")+1 {
		t.Errorf("boundary Position = %d, want %d", boundaries[0].Position, len("aaa")+1+len("bbb")+1)
	}
	if boundaries[0].Score != 0.3 {
		t.Errorf("boundary Score = %f, want 0.3", boundaries[0].Score)
	}
}

func TestFindSemanticBoundaries_AllAboveThreshold(t *testing.T) {
	t.Parallel()
	paragraphs := []string{"first", "second", "third"}
	sim := func(a, b string) float64 { return 0.9 }

	boundaries := FindSemanticBoundaries(paragraphs, sim, 0.5)
	if len(boundaries) != 0 {
		t.Errorf("expected 0 boundaries when all similar, got %d", len(boundaries))
	}
}

func TestRegistry_List_ReturnsCopy(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(&GenericChunker{})

	original := r.List()
	modified := r.List()
	if len(original) != len(modified) {
		t.Fatalf("list length mismatch: %d vs %d", len(original), len(modified))
	}

	modified[0] = nil
	again := r.List()
	if again[0] == nil {
		t.Error("List() returned reference to internal slice, not a copy")
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(&GoChunker{})
	r.Register(&GenericChunker{})

	c := r.Get("main.go")
	if c == nil {
		t.Fatal("Get(main.go) returned nil")
	}
	if c.Name() != "go" {
		t.Errorf("Get(main.go) = %q, want %q", c.Name(), "go")
	}

	generic := r.Get("readme.md")
	if generic == nil {
		t.Fatal("Get(readme.md) returned nil")
	}
	if generic.Name() != "generic" {
		t.Errorf("Get(readme.md) = %q, want %q", generic.Name(), "generic")
	}
}

func TestGoChunker(t *testing.T) {
	t.Parallel()
	c := &GoChunker{}
	if c.Name() != "go" {
		t.Errorf("Name() = %q, want %q", c.Name(), "go")
	}
	if !c.Supports("main.go") {
		t.Error("Supports(main.go) = false, want true")
	}
	if !c.Supports("pkg/Handler.GO") {
		t.Error("Supports(Handler.GO) = false, want true")
	}
	if c.Supports("main.py") {
		t.Error("Supports(main.py) = true, want false")
	}
	if c.Supports("Makefile") {
		t.Error("Supports(Makefile) = true, want false")
	}
	if c.Priority() != 100 {
		t.Errorf("Priority() = %d, want 100", c.Priority())
	}
}

func TestCodeChunker(t *testing.T) {
	t.Parallel()
	c := &CodeChunker{}
	if c.Name() != "code" {
		t.Errorf("Name() = %q, want %q", c.Name(), "code")
	}
	if !c.Supports("app.py") {
		t.Error("Supports(app.py) = false, want true")
	}
	if !c.Supports("index.tsx") {
		t.Error("Supports(index.tsx) = false, want true")
	}
	if !c.Supports("main.rs") {
		t.Error("Supports(main.rs) = false, want true")
	}
	if c.Supports("main.go") {
		t.Error("Supports(main.go) = true, want false (GoChunker handles .go)")
	}
	if c.Supports("readme.md") {
		t.Error("Supports(readme.md) = true, want false")
	}
	if c.Priority() != 50 {
		t.Errorf("Priority() = %d, want 50", c.Priority())
	}
}

func TestGenericChunker(t *testing.T) {
	t.Parallel()
	c := &GenericChunker{}
	if c.Name() != "generic" {
		t.Errorf("Name() = %q, want %q", c.Name(), "generic")
	}
	if !c.Supports("anything.txt") {
		t.Error("Supports(anything.txt) = false, want true")
	}
	if !c.Supports("") {
		t.Error("Supports('') = false, want true")
	}
	if c.Priority() != 0 {
		t.Errorf("Priority() = %d, want 0", c.Priority())
	}
}

func TestRegisterChunker_AddsToDefaultRegistry(t *testing.T) {
	type testChunker struct {
		GenericChunker
	}

	tc := &testChunker{}

	original := DefaultRegistry.List()
	originalCount := len(original)

	RegisterChunker(tc)

	list := DefaultRegistry.List()
	if len(list) != originalCount+1 {
		t.Errorf("expected %d chunkers after RegisterChunker, got %d", originalCount+1, len(list))
	}

	found := false
	for _, c := range list {
		if c == tc {
			found = true
			break
		}
	}
	if !found {
		t.Error("registered chunker not found in DefaultRegistry")
	}
}
