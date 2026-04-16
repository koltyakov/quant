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

	preamble := goPreamble(fset, f, lines)

	var decls []string
	for _, decl := range f.Decls {
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

	charBudget := codeCharBudget(chunkSize)
	preambleChars := runeCount(preamble)

	var chunks []Chunk
	var current []string
	currentChars := 0

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
		declChars := runeCount(decl)
		if declChars > charBudget {
			flush()
			current = nil
			currentChars = 0

			signature := goDeclSignature(decl)
			subChunks := Split(decl, chunkSize, overlapFraction)
			for _, sc := range subChunks {
				body := sc.Content
				content := body
				if preamble != "" {
					content = preamble + "\n\n" + body
				}
				if signature != "" && !strings.HasPrefix(strings.TrimSpace(body), signature) {
					content = signature + "\n\n" + content
				}
				chunks = append(chunks, Chunk{
					Content: content,
					Index:   len(chunks),
				})
			}
			continue
		}

		effectiveBudget := charBudget
		if len(current) == 0 {
			effectiveBudget -= preambleChars + 2 // account for preamble + "\n\n"
		}

		if currentChars > 0 && currentChars+declChars > effectiveBudget {
			flush()
			current = nil
			currentChars = 0
		}

		current = append(current, decl)
		currentChars += declChars
	}

	flush()
	return chunks
}

// goPreamble extracts the package clause and import declarations as a reusable header.
func goDeclSignature(decl string) string {
	for line := range strings.SplitSeq(decl, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > 120 {
				return trimmed[:120] + "..."
			}
			return trimmed
		}
	}
	return ""
}

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
