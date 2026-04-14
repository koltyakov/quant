//go:build treesitter

package chunk

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type tsDeclSpec struct {
	lang          *sitter.Language
	declTypes     map[string]bool
	preambleTypes map[string]bool
	attrTypes     map[string]bool
}

func tsChunk(src string, spec tsDeclSpec, chunkSize int, overlapFraction float64) []Chunk {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.lang)

	source := []byte(src)
	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	root := tree.RootNode()

	var preambleParts []string
	var decls []string
	var pendingAttr string

	n := int(root.NamedChildCount())
	for i := 0; i < n; i++ {
		child := root.NamedChild(i)
		nodeType := child.Type()

		if spec.preambleTypes[nodeType] {
			text := strings.TrimSpace(child.Content(source))
			if text != "" {
				preambleParts = append(preambleParts, text)
			}
			continue
		}

		if spec.attrTypes[nodeType] {
			pendingAttr += child.Content(source) + "\n"
			continue
		}

		if spec.declTypes[nodeType] {
			text := child.Content(source)
			if pendingAttr != "" {
				text = pendingAttr + text
				pendingAttr = ""
			}
			if strings.TrimSpace(text) != "" {
				decls = append(decls, text)
			}
			continue
		}

		pendingAttr = ""
	}

	if len(decls) == 0 {
		return nil
	}

	preamble := strings.Join(preambleParts, "\n")
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

			signature := tsSignature(decl)
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
			effectiveBudget -= preambleChars + 2
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

func tsSignature(block string) string {
	for _, line := range strings.Split(block, "\n") {
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
