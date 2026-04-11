package chunk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// splitGo splits Go source code into chunks aligned with top-level declarations.
// Package declaration + imports form a preamble that is prepended to each chunk.
// If parsing fails, returns nil so the caller can fall back to generic splitting.
func splitGo(src string, chunkSize int, overlapFraction float64) []Chunk {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil
	}

	lines := strings.Split(src, "\n")

	// Build the preamble: package declaration + import block.
	preamble := goPreamble(fset, f, lines)

	// Collect top-level declaration text blocks.
	var decls []string
	for _, decl := range f.Decls {
		// Skip import declarations — already in preamble.
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		start := fset.Position(decl.Pos()).Line
		end := fset.Position(decl.End()).Line
		if start < 1 || end > len(lines) {
			continue
		}
		block := strings.Join(lines[start-1:end], "\n")
		if strings.TrimSpace(block) != "" {
			decls = append(decls, block)
		}
	}

	if len(decls) == 0 {
		return nil
	}

	// Merge adjacent small declarations up to chunkSize, prepending preamble to each chunk.
	// Overlap is intentionally skipped for code chunks — declaration boundaries are cleaner
	// split points than word-count overlaps.
	var chunks []Chunk
	var current []string
	currentWords := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		body := strings.Join(current, "\n\n")
		content := body
		if preamble != "" {
			content = preamble + "\n\n" + body
		}
		if strings.TrimSpace(content) == "" {
			return
		}
		chunks = append(chunks, Chunk{
			Content: content,
			Index:   len(chunks),
		})
	}

	for _, decl := range decls {
		declWords := wordCount(decl)
		if declWords > chunkSize {
			// Oversized single declaration: flush current, then split the decl standalone.
			flush()
			current = nil
			currentWords = 0

			subChunks := Split(decl, chunkSize, overlapFraction)
			for _, sc := range subChunks {
				body := sc.Content
				content := body
				if preamble != "" {
					content = preamble + "\n\n" + body
				}
				chunks = append(chunks, Chunk{
					Content: content,
					Index:   len(chunks),
				})
			}
			continue
		}

		if currentWords > 0 && currentWords+declWords > chunkSize {
			flush()
			// Overlap: keep last few words from previous chunk (not easily extractable
			// from declaration blocks, so we skip overlap for code chunks).
			current = nil
			currentWords = 0
		}

		current = append(current, decl)
		currentWords += declWords
	}

	flush()
	return chunks
}

// goPreamble extracts the package clause and import declarations as a reusable header.
func goPreamble(fset *token.FileSet, f *ast.File, lines []string) string {
	var parts []string

	// Package line.
	pkgLine := fset.Position(f.Package).Line
	if pkgLine >= 1 && pkgLine <= len(lines) {
		parts = append(parts, strings.TrimSpace(lines[pkgLine-1]))
	}

	// Import declarations.
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		start := fset.Position(gd.Pos()).Line
		end := fset.Position(gd.End()).Line
		if start < 1 || end > len(lines) {
			continue
		}
		block := strings.Join(lines[start-1:end], "\n")
		if strings.TrimSpace(block) != "" {
			parts = append(parts, block)
		}
	}

	return strings.Join(parts, "\n\n")
}
