//go:build treesitter

package chunk

import (
	"path/filepath"
	"strings"

	"github.com/smacker/go-tree-sitter/rust"
)

type RustChunker struct{}

func (c *RustChunker) Name() string { return "rust" }

func (c *RustChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".rs"
}

func (c *RustChunker) Priority() int { return 75 }

func (c *RustChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: rust.GetLanguage(),
		declTypes: map[string]bool{
			"function_item":        true,
			"struct_item":          true,
			"enum_item":            true,
			"union_item":           true,
			"impl_item":            true,
			"trait_item":           true,
			"mod_item":             true,
			"const_item":           true,
			"static_item":          true,
			"type_item":            true,
			"foreign_mod_item":     true,
			"macro_definition":     true,
			"macro_invocation":     true,
			"expression_item":      true,
			"attribute_item":       true,
			"inner_attribute_item": true,
		},
		preambleTypes: map[string]bool{
			"use_declaration": true,
		},
		attrTypes: map[string]bool{},
	}, chunkSize, overlapFraction)
}

func init() {
	DefaultRegistry.Register(&RustChunker{})
}
