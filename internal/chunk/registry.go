package chunk

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Chunker defines the interface for text chunking strategies.
// Implementations can provide specialized chunking for different file types.
type Chunker interface {
	// Name returns a human-readable name for this chunker.
	Name() string

	// Supports returns true if this chunker can handle the given file path.
	// The path is used to determine file type (e.g., by extension).
	Supports(path string) bool

	// Priority returns the priority of this chunker. Higher values take precedence
	// when multiple chunkers support the same file type.
	Priority() int

	// Split divides text into chunks according to this strategy.
	// chunkSize is the target size in words, overlapFraction is 0-1.
	Split(text string, chunkSize int, overlapFraction float64) []Chunk
}

// Registry holds registered chunkers and selects the appropriate one for each file.
type Registry struct {
	mu       sync.RWMutex
	chunkers []Chunker
	sorted   bool
}

// DefaultRegistry is the global chunker registry with built-in chunkers.
var DefaultRegistry = NewRegistry()

func init() {
	// Register built-in chunkers with the default registry.
	DefaultRegistry.Register(&GoChunker{})
	DefaultRegistry.Register(&CodeChunker{})
	DefaultRegistry.Register(&GenericChunker{})
}

// NewRegistry creates a new empty chunker registry.
func NewRegistry() *Registry {
	return &Registry{
		chunkers: make([]Chunker, 0),
	}
}

// Register adds a chunker to the registry.
func (r *Registry) Register(c Chunker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunkers = append(r.chunkers, c)
	r.sorted = false
}

// Get returns the highest-priority chunker that supports the given path.
// Returns nil if no chunker supports the path.
func (r *Registry) Get(path string) Chunker {
	r.mu.RLock()
	if !r.sorted {
		r.mu.RUnlock()
		r.sort()
		r.mu.RLock()
	}
	defer r.mu.RUnlock()

	for _, c := range r.chunkers {
		if c.Supports(path) {
			return c
		}
	}
	return nil
}

// Split finds the appropriate chunker for the path and splits the text.
// Falls back to generic splitting if no specialized chunker is found.
func (r *Registry) Split(text, path string, chunkSize int, overlapFraction float64) []Chunk {
	chunker := r.Get(path)
	if chunker == nil {
		return Split(text, chunkSize, overlapFraction)
	}
	chunks := chunker.Split(text, chunkSize, overlapFraction)
	if chunks == nil {
		// Chunker returned nil (e.g., parse error), fall back to generic.
		return Split(text, chunkSize, overlapFraction)
	}
	return chunks
}

func (r *Registry) sort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sorted {
		return
	}
	sort.Slice(r.chunkers, func(i, j int) bool {
		return r.chunkers[i].Priority() > r.chunkers[j].Priority()
	})
	r.sorted = true
}

// List returns all registered chunkers (for debugging/introspection).
func (r *Registry) List() []Chunker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Chunker, len(r.chunkers))
	copy(result, r.chunkers)
	return result
}

// GoChunker handles Go source files with AST-aware chunking.
type GoChunker struct{}

func (c *GoChunker) Name() string { return "go" }

func (c *GoChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".go"
}

func (c *GoChunker) Priority() int { return 100 }

func (c *GoChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return splitGo(text, chunkSize, overlapFraction)
}

// CodeChunker handles common programming languages with heuristic-based chunking.
type CodeChunker struct{}

// codeExtensions lists file extensions handled by the CodeChunker.
var codeExtensions = map[string]bool{
	".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".rs": true, ".java": true, ".c": true, ".cpp": true, ".cc": true,
	".h": true, ".hpp": true, ".rb": true, ".php": true, ".swift": true,
	".kt": true, ".cs": true, ".scala": true, ".lua": true,
	".ex": true, ".exs": true,
}

func (c *CodeChunker) Name() string { return "code" }

func (c *CodeChunker) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return codeExtensions[ext]
}

func (c *CodeChunker) Priority() int { return 50 }

func (c *CodeChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return splitCode(text, chunkSize, overlapFraction)
}

// GenericChunker handles all file types with paragraph-based chunking.
type GenericChunker struct{}

func (c *GenericChunker) Name() string { return "generic" }

func (c *GenericChunker) Supports(path string) bool {
	return true // Fallback for all files
}

func (c *GenericChunker) Priority() int { return 0 }

func (c *GenericChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return Split(text, chunkSize, overlapFraction)
}

// RegisterChunker adds a custom chunker to the default registry.
func RegisterChunker(c Chunker) {
	DefaultRegistry.Register(c)
}
