package mcp

import (
	"fmt"
	"strings"

	"github.com/koltyakov/quant/internal/index"
)

// Rendering helpers shared by the tool handlers in tools.go: they turn index
// records into the JSON rows and the human-readable text bodies that make up
// an MCP tool response. Nothing here touches the store or the server state.

func searchRows(results []index.SearchResult) []searchToolResultRow {
	rows := make([]searchToolResultRow, 0, len(results))
	for _, result := range results {
		rows = append(rows, searchRow(result))
	}
	return rows
}

func searchRow(result index.SearchResult) searchToolResultRow {
	return searchToolResultRow{
		ChunkID:       result.ChunkID,
		Path:          result.DocumentPath,
		ChunkIndex:    result.ChunkIndex,
		Score:         result.Score,
		ScoreKind:     result.ScoreKind,
		ChunkContent:  result.ChunkContent,
		ParentID:      result.ParentID,
		Depth:         result.Depth,
		SectionTitle:  result.SectionTitle,
		ParentContext: result.ParentContext,
	}
}

func listSourceRows(docs []index.Document) []listSourcesResultRow {
	rows := make([]listSourcesResultRow, 0, len(docs))
	for _, doc := range docs {
		rows = append(rows, listSourcesResultRow{
			Path:      doc.Path,
			IndexedAt: doc.IndexedAt,
		})
	}
	return rows
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func summarizeLogText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func formatSearchSpotlights(results []index.SearchResult, limit int) string {
	if len(results) == 0 {
		return "none"
	}
	if limit <= 0 || limit > len(results) {
		limit = len(results)
	}

	parts := make([]string, 0, limit)
	for _, r := range results[:limit] {
		parts = append(parts, fmt.Sprintf(
			"%s#%d score=%.4f %s snippet=%q",
			r.DocumentPath,
			r.ChunkIndex,
			r.Score,
			r.ScoreKind,
			summarizeLogText(r.ChunkContent, 72),
		))
	}
	return strings.Join(parts, " | ")
}

func formatDocumentSpotlights(docs []index.Document, limit int) string {
	if len(docs) == 0 {
		return "none"
	}
	if limit <= 0 || limit > len(docs) {
		limit = len(docs)
	}

	parts := make([]string, 0, limit)
	for _, d := range docs[:limit] {
		parts = append(parts, d.Path)
	}
	return strings.Join(parts, ", ")
}

func formatSearchResults(results []index.SearchResult) string {
	if len(results) == 0 {
		return "No results found."
	}

	var b strings.Builder
	remaining := maxSearchOutputRunes
	rendered := 0

	for i, r := range results {
		entry := renderSearchResultEntry(i+1, r, maxResultSnippetRunes)
		entryRunes := len([]rune(entry))
		if entryRunes > remaining {
			if rendered == 0 {
				entry = renderSearchResultEntry(i+1, r, entrySnippetBudget(r, remaining))
				entryRunes = len([]rune(entry))
				if entryRunes > remaining {
					entry = truncateRunes(entry, remaining)
					entryRunes = len([]rune(entry))
				}
				if entryRunes > 0 {
					b.WriteString(entry)
					remaining -= entryRunes
					rendered++
				}
			}
			break
		}

		b.WriteString(entry)
		remaining -= entryRunes
		rendered++
	}

	if omitted := len(results) - rendered; omitted > 0 && remaining > 0 {
		footer := fmt.Sprintf("[omitted %d additional result(s) to stay within the output budget]\n", omitted)
		if len([]rune(footer)) > remaining {
			footer = truncateRunes(footer, remaining)
		}
		b.WriteString(footer)
	}

	return b.String()
}

func renderSearchResultEntry(position int, result index.SearchResult, snippetLimit int) string {
	header := fmt.Sprintf(
		"--- Result %d (score: %.4f, kind: %s, chunk_id: %d) ---\nFile: %s (chunk %d)\n",
		position,
		result.Score,
		result.ScoreKind,
		result.ChunkID,
		result.DocumentPath,
		result.ChunkIndex,
	)

	content := strings.TrimSpace(result.ChunkContent)
	snippet, truncated := truncateRunesWithFlag(content, snippetLimit)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(snippet)
	if truncated {
		b.WriteString("\n[chunk content truncated]")
	}
	b.WriteString("\n\n")
	return b.String()
}

func entrySnippetBudget(result index.SearchResult, totalBudget int) int {
	header := fmt.Sprintf(
		"--- Result %d (score: %.4f, kind: %s, chunk_id: %d) ---\nFile: %s (chunk %d)\n",
		1,
		result.Score,
		result.ScoreKind,
		result.ChunkID,
		result.DocumentPath,
		result.ChunkIndex,
	)
	reserved := len([]rune(header)) + len([]rune("\n[chunk content truncated]\n\n"))
	if totalBudget <= reserved {
		return 0
	}
	return totalBudget - reserved
}

func truncateRunesWithFlag(text string, limit int) (string, bool) {
	if limit <= 0 {
		return "", strings.TrimSpace(text) != ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

func truncateRunes(text string, limit int) string {
	truncated, _ := truncateRunesWithFlag(text, limit)
	return truncated
}
