//go:build treesitter

package chunk

import (
	"path/filepath"
	"strings"

	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/kotlin"
)

type JavaChunker struct{}

func (c *JavaChunker) Name() string { return "java" }

func (c *JavaChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".java"
}

func (c *JavaChunker) Priority() int { return 75 }

func (c *JavaChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: java.GetLanguage(),
		declTypes: map[string]bool{
			"class_declaration":           true,
			"interface_declaration":       true,
			"enum_declaration":            true,
			"record_declaration":          true,
			"module_declaration":          true,
			"annotation_type_declaration": true,
		},
		preambleTypes: map[string]bool{
			"package_declaration": true,
			"import_declaration":  true,
		},
	}, chunkSize, overlapFraction)
}

type KotlinChunker struct{}

func (c *KotlinChunker) Name() string { return "kotlin" }

func (c *KotlinChunker) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".kt" || strings.ToLower(filepath.Ext(path)) == ".kts"
}

func (c *KotlinChunker) Priority() int { return 75 }

func (c *KotlinChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return tsChunk(text, tsDeclSpec{
		lang: kotlin.GetLanguage(),
		declTypes: map[string]bool{
			"class_declaration":     true,
			"object_declaration":    true,
			"interface_declaration": true,
			"enum_declaration":      true,
			"function_declaration":  true,
			"property_declaration":  true,
			"type_alias":            true,
		},
		preambleTypes: map[string]bool{
			"package_header": true,
			"import_header":  true,
			"import_list":    true,
		},
	}, chunkSize, overlapFraction)
}

func init() {
	DefaultRegistry.Register(&JavaChunker{})
	DefaultRegistry.Register(&KotlinChunker{})
}
