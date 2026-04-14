//go:build treesitter

package chunk

import (
	"path/filepath"
	"strings"

	clang "github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
)

type CChunker struct{}

func (ck *CChunker) Name() string { return "c" }

func (ck *CChunker) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".c" || ext == ".h"
}

func (ck *CChunker) Priority() int { return 75 }

func (ck *CChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: clang.GetLanguage(),
		declTypes: map[string]bool{
			"function_definition":  true,
			"struct_specifier":     true,
			"enum_specifier":       true,
			"union_specifier":      true,
			"declaration":          true,
			"type_definition":      true,
			"preproc_def":          true,
			"preproc_function_def": true,
		},
		preambleTypes: map[string]bool{
			"preproc_include": true,
		},
	}, chunkSize, overlapFraction)
}

type CPPChunker struct{}

func (ck *CPPChunker) Name() string { return "cpp" }

func (ck *CPPChunker) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cpp" || ext == ".cc" || ext == ".cxx" || ext == ".hpp" || ext == ".hxx"
}

func (ck *CPPChunker) Priority() int { return 75 }

func (ck *CPPChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: cpp.GetLanguage(),
		declTypes: map[string]bool{
			"function_definition":  true,
			"struct_specifier":     true,
			"class_specifier":      true,
			"enum_specifier":       true,
			"union_specifier":      true,
			"namespace_definition": true,
			"declaration":          true,
			"type_definition":      true,
			"template_declaration": true,
			"concept_definition":   true,
			"preproc_def":          true,
			"preproc_function_def": true,
		},
		preambleTypes: map[string]bool{
			"preproc_include": true,
		},
	}, chunkSize, overlapFraction)
}

func init() {
	DefaultRegistry.Register(&CChunker{})
	DefaultRegistry.Register(&CPPChunker{})
}
