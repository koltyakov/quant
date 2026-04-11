package chunk

import (
	"path/filepath"
	"strings"
	"unicode"
)

// SplitWithPath splits text into chunks using a code-aware strategy when the
// file path indicates a source-code file, falling back to the generic paragraph
// splitter for all other content.
func SplitWithPath(text string, path string, chunkSize int, overlapFraction float64) []Chunk {
	if path != "" {
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go":
			if chunks := splitGo(text, chunkSize, overlapFraction); chunks != nil {
				return chunks
			}
		case ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".c", ".cpp", ".cc", ".h", ".hpp",
			".rb", ".php", ".swift", ".kt", ".cs", ".scala", ".lua", ".ex", ".exs":
			if chunks := splitCode(text, chunkSize, overlapFraction); chunks != nil {
				return chunks
			}
		}
	}
	return Split(text, chunkSize, overlapFraction)
}

type Chunk struct {
	Content string
	Index   int
	Heading string // most-recent heading active when this chunk was created (may be empty)
}

func Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	if strings.TrimSpace(text) == "" || chunkSize <= 0 {
		return nil
	}

	units := splitUnits(normalizeLineEndings(text))
	if len(units) == 0 {
		return nil
	}

	units = expandLargeUnits(units, chunkSize)
	overlapWords := int(float64(chunkSize) * overlapFraction)
	if overlapWords >= chunkSize {
		overlapWords = chunkSize / 4
	}

	// Track nested headings for breadcrumb context propagation.
	var headingStack headingBreadcrumbs

	var chunks []Chunk
	var current []string
	currentWords := 0

	flush := func() {
		content := joinUnits(current)
		if strings.TrimSpace(content) == "" {
			return
		}
		breadcrumb := headingStack.breadcrumb()
		// Prepend breadcrumb context if the chunk doesn't already start with a heading.
		if breadcrumb != "" && !startsWithHeading(content) {
			content = breadcrumb + "\n\n" + content
		}
		chunks = append(chunks, Chunk{
			Content: content,
			Index:   len(chunks),
			Heading: breadcrumb,
		})
	}

	for _, unit := range units {
		unitWords := wordCount(unit)
		if unitWords == 0 {
			continue
		}

		// Track the most recent heading at each depth level.
		trimmed := strings.TrimSpace(unit)
		if isHeading(trimmed) {
			headingStack.push(trimmed)
		}

		if currentWords > 0 && currentWords+unitWords > chunkSize {
			flush()
			overlap := lastWords(joinUnits(current), overlapWords)
			current = nil
			currentWords = 0
			if overlap != "" {
				current = append(current, overlap)
				currentWords = wordCount(overlap)
			}
		}

		current = append(current, strings.TrimSpace(unit))
		currentWords += unitWords
	}

	if currentWords > 0 {
		flush()
	}

	return chunks
}

// headingBreadcrumbs maintains a stack of markdown headings by depth level,
// producing a compact "Section > Subsection > Sub-subsection" breadcrumb.
type headingBreadcrumbs struct {
	// levels[i] holds the heading text (without # prefix) at depth i+1.
	// When a new heading at depth N is pushed, all deeper levels are cleared.
	levels [6]string
	depth  int // deepest populated level (1-based); 0 = empty
}

// push records a markdown heading, clearing any deeper headings.
func (h *headingBreadcrumbs) push(heading string) {
	d := headingDepth(heading)
	if d < 1 || d > 6 {
		return
	}
	text := strings.TrimSpace(strings.TrimLeft(heading, "#"))
	h.levels[d-1] = text
	// Clear all deeper levels.
	for i := d; i < 6; i++ {
		h.levels[i] = ""
	}
	h.depth = d
}

// breadcrumb returns the current heading context as "H1 > H2 > H3".
// Returns empty string if no headings have been seen.
func (h *headingBreadcrumbs) breadcrumb() string {
	var parts []string
	for i := 0; i < 6; i++ {
		if h.levels[i] != "" {
			parts = append(parts, h.levels[i])
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		// Single heading: preserve original markdown format for clarity.
		return strings.Repeat("#", h.singleDepth()) + " " + parts[0]
	}
	return strings.Join(parts, " > ")
}

// singleDepth returns the depth of the single populated heading, for preserving
// the # prefix when there's only one level.
func (h *headingBreadcrumbs) singleDepth() int {
	for i := 0; i < 6; i++ {
		if h.levels[i] != "" {
			return i + 1
		}
	}
	return 1
}

// headingDepth returns the depth of a markdown heading (number of leading # chars).
func headingDepth(line string) int {
	n := 0
	for _, r := range line {
		if r == '#' {
			n++
		} else {
			break
		}
	}
	return n
}

func splitUnits(text string) []string {
	lines := strings.Split(text, "\n")
	var units []string
	var paragraph []string
	inFence := false
	var fence []string

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		units = append(units, strings.Join(paragraph, "\n"))
		paragraph = nil
	}

	flushFence := func() {
		if len(fence) == 0 {
			return
		}
		units = append(units, strings.Join(fence, "\n"))
		fence = nil
	}

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			fence = append(fence, line)
			if inFence {
				flushFence()
			}
			inFence = !inFence
			continue
		}

		if inFence {
			fence = append(fence, line)
			continue
		}

		if trimmed == "" {
			flushParagraph()
			continue
		}

		if isStandaloneMarker(trimmed) {
			flushParagraph()
			units = append(units, trimmed)
			continue
		}

		paragraph = append(paragraph, line)
	}

	flushParagraph()
	flushFence()

	return units
}

func expandLargeUnits(units []string, chunkSize int) []string {
	var out []string
	for _, unit := range units {
		if wordCount(unit) <= chunkSize {
			out = append(out, unit)
			continue
		}
		out = append(out, splitLargeUnit(unit, chunkSize)...)
	}
	return out
}

func splitLargeUnit(unit string, chunkSize int) []string {
	if strings.Contains(unit, "\n") {
		lines := strings.Split(unit, "\n")
		var parts []string
		var current []string
		currentWords := 0
		for _, line := range lines {
			lineWords := wordCount(line)
			if currentWords > 0 && currentWords+lineWords > chunkSize {
				parts = append(parts, strings.Join(current, "\n"))
				current = nil
				currentWords = 0
			}
			current = append(current, line)
			currentWords += lineWords
		}
		if currentWords > 0 {
			parts = append(parts, strings.Join(current, "\n"))
		}
		if len(parts) > 1 {
			return parts
		}
	}

	sentences := splitSentences(unit)
	if len(sentences) > 1 {
		var parts []string
		var current []string
		currentWords := 0
		for _, sentence := range sentences {
			sentenceWords := wordCount(sentence)
			if currentWords > 0 && currentWords+sentenceWords > chunkSize {
				parts = append(parts, strings.Join(current, " "))
				current = nil
				currentWords = 0
			}
			current = append(current, sentence)
			currentWords += sentenceWords
		}
		if currentWords > 0 {
			parts = append(parts, strings.Join(current, " "))
		}
		if len(parts) > 1 {
			return parts
		}
	}

	words := strings.Fields(unit)
	var parts []string
	for start := 0; start < len(words); start += chunkSize {
		end := start + chunkSize
		if end > len(words) {
			end = len(words)
		}
		parts = append(parts, strings.Join(words[start:end], " "))
	}
	return parts
}

// splitSentences splits text into sentences with abbreviation awareness.
// It avoids splitting on periods after common abbreviations like "Dr.", "U.S.", etc.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	runes := []rune(text)
	var sentences []string
	start := 0

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r != '.' && r != '!' && r != '?' {
			continue
		}

		// Check for ellipsis (... or more dots) - don't split.
		if r == '.' && i+1 < len(runes) && runes[i+1] == '.' {
			continue
		}

		// For periods, check if this looks like an abbreviation.
		if r == '.' && isAbbreviationPeriod(runes, i) {
			continue
		}

		// Must be followed by whitespace or end-of-string to be a sentence boundary.
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}

		sentence := strings.TrimSpace(string(runes[start : i+1]))
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
		start = i + 1
	}

	// Remaining text.
	if start < len(runes) {
		remainder := strings.TrimSpace(string(runes[start:]))
		if remainder != "" {
			sentences = append(sentences, remainder)
		}
	}

	return sentences
}

// Common abbreviations that should not trigger sentence splits.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
	"sr": true, "jr": true, "st": true, "vs": true, "etc": true,
	"inc": true, "ltd": true, "corp": true, "co": true, "dept": true,
	"gen": true, "gov": true, "sgt": true, "cpl": true, "pvt": true,
	"rev": true, "hon": true, "pres": true, "mgr": true, "supt": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "sept": true, "oct": true,
	"nov": true, "dec": true,
	"fig": true, "eq": true, "vol": true, "no": true, "op": true,
	"approx": true, "est": true, "min": true, "max": true,
}

// isAbbreviationPeriod checks whether the period at runes[pos] is part of an
// abbreviation rather than a sentence terminator.
func isAbbreviationPeriod(runes []rune, pos int) bool {
	// Single-letter abbreviations: "U.S.", "e.g.", "i.e.", "A."
	if pos >= 1 && unicode.IsLetter(runes[pos-1]) {
		// Check if preceded by a single letter (possibly after another abbreviation period).
		if pos < 2 || !unicode.IsLetter(runes[pos-2]) {
			return true
		}
	}

	// Multi-letter abbreviations: extract the preceding word.
	wordStart := pos - 1
	for wordStart >= 0 && unicode.IsLetter(runes[wordStart]) {
		wordStart--
	}
	wordStart++

	if wordStart < pos {
		word := strings.ToLower(string(runes[wordStart:pos]))
		if abbreviations[word] {
			return true
		}
	}

	// Decimal numbers: "3.14"
	if pos >= 1 && unicode.IsDigit(runes[pos-1]) && pos+1 < len(runes) && unicode.IsDigit(runes[pos+1]) {
		return true
	}

	return false
}

func joinUnits(units []string) string {
	if len(units) == 0 {
		return ""
	}
	trimmed := make([]string, 0, len(units))
	for _, unit := range units {
		unit = strings.TrimSpace(unit)
		if unit != "" {
			trimmed = append(trimmed, unit)
		}
	}
	return strings.TrimSpace(strings.Join(trimmed, "\n\n"))
}

func isStandaloneMarker(line string) bool {
	return isHeading(line) || isDocumentMarker(line)
}

// isHeading returns true for markdown headings.
func isHeading(line string) bool {
	if !strings.HasPrefix(line, "#") {
		return false
	}
	if strings.HasPrefix(line, "#!") {
		return false
	}
	return true
}

// isDocumentMarker returns true for structural document markers like [Page N], [Slide N], etc.
func isDocumentMarker(line string) bool {
	return strings.HasPrefix(line, "[Page ") ||
		strings.HasPrefix(line, "[Slide ") ||
		strings.HasPrefix(line, "[Sheet ") ||
		strings.HasPrefix(line, "[Document]") ||
		strings.HasPrefix(line, "[Header ") ||
		strings.HasPrefix(line, "[Footer ")
}

// startsWithHeading checks if the content begins with a markdown heading line.
func startsWithHeading(content string) bool {
	firstLine, _, _ := strings.Cut(content, "\n")
	return isHeading(strings.TrimSpace(firstLine))
}

func lastWords(text string, n int) string {
	if n <= 0 {
		return ""
	}
	words := strings.Fields(text)
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[len(words)-n:], " ")
}

func wordCount(text string) int {
	return len(strings.Fields(text))
}

func runeCount(text string) int {
	return len([]rune(text))
}

// codeCharBudget converts a word-based chunk size to a character budget for
// code files. Code has different "word" boundaries than prose (braces,
// operators, semicolons each count as words via strings.Fields), making
// wordCount unreliable. A character budget produces more consistent chunk sizes.
const avgCodeCharsPerWord = 5

func codeCharBudget(chunkSize int) int {
	return chunkSize * avgCodeCharsPerWord
}

func normalizeLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}
