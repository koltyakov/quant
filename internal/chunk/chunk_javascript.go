//go:build treesitter

package chunk

import (
	"path/filepath"
	"strings"

	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	tsgrammar "github.com/smacker/go-tree-sitter/typescript/typescript"
)

type JavaScriptChunker struct{}

func (c *JavaScriptChunker) Name() string { return "javascript" }

func (c *JavaScriptChunker) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs"
}

func (c *JavaScriptChunker) Priority() int { return 75 }

func (c *JavaScriptChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: javascript.GetLanguage(),
		declTypes: map[string]bool{
			"function_declaration":           true,
			"generator_function_declaration": true,
			"class_declaration":              true,
			"lexical_declaration":            true,
			"variable_declaration":           true,
			"export_statement":               true,
		},
		preambleTypes: map[string]bool{
			"import_statement": true,
		},
	}, chunkSize, overlapFraction)
}

type TypeScriptChunker struct{}

func (c *TypeScriptChunker) Name() string { return "typescript" }

func (c *TypeScriptChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".ts"
}

func (c *TypeScriptChunker) Priority() int { return 75 }

func (c *TypeScriptChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: tsgrammar.GetLanguage(),
		declTypes: map[string]bool{
			"function_declaration":           true,
			"generator_function_declaration": true,
			"class_declaration":              true,
			"lexical_declaration":            true,
			"variable_declaration":           true,
			"export_statement":               true,
			"interface_declaration":          true,
			"type_alias_declaration":         true,
			"enum_declaration":               true,
			"ambient_declaration":            true,
			"module":                         true,
			"abstract_class_declaration":     true,
		},
		preambleTypes: map[string]bool{
			"import_statement": true,
		},
	}, chunkSize, overlapFraction)
}

type TSXChunker struct{}

func (c *TSXChunker) Name() string { return "tsx" }

func (c *TSXChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".tsx"
}

func (c *TSXChunker) Priority() int { return 75 }

func (c *TSXChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: tsx.GetLanguage(),
		declTypes: map[string]bool{
			"function_declaration":           true,
			"generator_function_declaration": true,
			"class_declaration":              true,
			"lexical_declaration":            true,
			"variable_declaration":           true,
			"export_statement":               true,
			"interface_declaration":          true,
			"type_alias_declaration":         true,
			"enum_declaration":               true,
			"ambient_declaration":            true,
			"module":                         true,
			"abstract_class_declaration":     true,
		},
		preambleTypes: map[string]bool{
			"import_statement": true,
		},
	}, chunkSize, overlapFraction)
}

func init() {
	DefaultRegistry.Register(&JavaScriptChunker{})
	DefaultRegistry.Register(&TypeScriptChunker{})
	DefaultRegistry.Register(&TSXChunker{})
}
