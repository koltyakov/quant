//go:build treesitter

package chunk

import (
	"path/filepath"
	"strings"

	"github.com/smacker/go-tree-sitter/python"
)

type PythonChunker struct{}

func (c *PythonChunker) Name() string { return "python" }

func (c *PythonChunker) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".py" || ext == ".pyw"
}

func (c *PythonChunker) Priority() int { return 75 }

func (c *PythonChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: python.GetLanguage(),
		declTypes: map[string]bool{
			"function_definition":  true,
			"class_definition":     true,
			"decorated_definition": true,
		},
		preambleTypes: map[string]bool{
			"import_statement":      true,
			"import_from_statement": true,
		},
	}, chunkSize, overlapFraction)
}

func init() {
	DefaultRegistry.Register(&PythonChunker{})
}
