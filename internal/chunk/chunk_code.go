package chunk

import (
	"regexp"
	"strings"
)

// topLevelBlockPattern matches lines that typically start a top-level declaration
// in common languages: Go, Python, JavaScript/TypeScript, Rust, Java, C/C++.
var topLevelBlockPattern = regexp.MustCompile(
	`^(func |class |def |export |public |private |protected |fn |impl |type |const |var |let |interface |enum |struct )`,
)

// splitCode splits source code using a heuristic: it identifies top-level block
// boundaries by looking for lines at indentation level 0 that match common
// declaration keywords. Adjacent small blocks are merged up to chunkSize.
// Returns nil if fewer than 2 boundaries are found (fall back to generic splitting).
func splitCode(src string, chunkSize int, overlapFraction float64) []Chunk {
	lines := strings.Split(src, "\n")
	boundaries := codeBlockBoundaries(lines)
	if len(boundaries) < 2 {
		return nil
	}

	// Extract text block for each boundary range.
	var blocks []string
	for i, start := range boundaries {
		end := len(lines)
		if i+1 < len(boundaries) {
			end = boundaries[i+1]
		}
		block := strings.Join(lines[start:end], "\n")
		block = strings.TrimRight(block, "\n\t ")
		if strings.TrimSpace(block) != "" {
			blocks = append(blocks, block)
		}
	}

	if len(blocks) == 0 {
		return nil
	}

	// Overlap is skipped for code chunks - declaration boundaries are cleaner split points.
	var chunks []Chunk
	var current []string
	currentWords := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		content := strings.Join(current, "\n\n")
		if strings.TrimSpace(content) == "" {
			return
		}
		chunks = append(chunks, Chunk{
			Content: content,
			Index:   len(chunks),
		})
	}

	for _, block := range blocks {
		blockWords := wordCount(block)
		if blockWords > chunkSize {
			flush()
			current = nil
			currentWords = 0
			subChunks := Split(block, chunkSize, overlapFraction)
			for _, sc := range subChunks {
				chunks = append(chunks, Chunk{
					Content: sc.Content,
					Index:   len(chunks),
				})
			}
			continue
		}

		if currentWords > 0 && currentWords+blockWords > chunkSize {
			flush()
			current = nil
			currentWords = 0
		}

		current = append(current, block)
		currentWords += blockWords
	}

	flush()
	return chunks
}

// codeBlockBoundaries returns the line indices (0-based) where top-level blocks start.
func codeBlockBoundaries(lines []string) []int {
	var boundaries []int
	inBlock := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Only consider lines at indentation level 0.
		if line[0] == ' ' || line[0] == '\t' {
			if !inBlock {
				inBlock = true
			}
			continue
		}
		// Line at indent 0.
		if topLevelBlockPattern.MatchString(trimmed) {
			boundaries = append(boundaries, i)
			inBlock = true
		} else if inBlock && trimmed != "" {
			// Non-matching line at indent 0 after a block (e.g., closing brace alone).
			inBlock = false
		}
	}
	return boundaries
}
