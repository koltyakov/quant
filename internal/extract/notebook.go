package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type NotebookExtractor struct{}

type notebookFile struct {
	Cells []notebookCell `json:"cells"`
}

type notebookCell struct {
	CellType string           `json:"cell_type"`
	Source   json.RawMessage  `json:"source"`
	Outputs  []notebookOutput `json:"outputs"`
}

type notebookOutput struct {
	Text json.RawMessage            `json:"text"`
	Data map[string]json.RawMessage `json:"data"`
}

func (n *NotebookExtractor) Extract(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var notebook notebookFile
	if err := json.Unmarshal(data, &notebook); err != nil {
		return "", fmt.Errorf("parsing notebook json: %w", err)
	}

	var sections []string
	for i, cell := range notebook.Cells {
		content := strings.TrimSpace(renderNotebookCell(cell))
		if content == "" {
			continue
		}

		label := fmt.Sprintf("[Cell %d]", i+1)
		switch cell.CellType {
		case "markdown":
			label = fmt.Sprintf("[Markdown Cell %d]", i+1)
		case "code":
			label = fmt.Sprintf("[Code Cell %d]", i+1)
		case "raw":
			label = fmt.Sprintf("[Raw Cell %d]", i+1)
		}
		sections = append(sections, label+"\n"+content)
	}

	return strings.Join(sections, "\n\n"), nil
}

func (n *NotebookExtractor) Supports(path string) bool {
	return strings.EqualFold(ext(path), ".ipynb")
}

func renderNotebookCell(cell notebookCell) string {
	source := strings.TrimSpace(notebookText(cell.Source))
	outputs := strings.TrimSpace(renderNotebookOutputs(cell.Outputs))

	switch {
	case source != "" && outputs != "":
		return source + "\n\n[Output]\n" + outputs
	case source != "":
		return source
	case outputs != "":
		return "[Output]\n" + outputs
	default:
		return ""
	}
}

func renderNotebookOutputs(outputs []notebookOutput) string {
	var parts []string
	seen := make(map[string]struct{})
	for _, output := range outputs {
		text := strings.TrimSpace(notebookText(output.Text))
		if text != "" {
			parts = appendNotebookOutput(parts, seen, text)
		}

		for _, key := range []string{"text/plain", "text/markdown"} {
			text = strings.TrimSpace(notebookText(output.Data[key]))
			if text != "" {
				parts = appendNotebookOutput(parts, seen, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func appendNotebookOutput(parts []string, seen map[string]struct{}, text string) []string {
	if text == "" {
		return parts
	}
	if _, ok := seen[text]; ok {
		return parts
	}
	seen[text] = struct{}{}
	return append(parts, text)
}

func notebookText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return cleanSpacing(single)
	}

	var lines []string
	if err := json.Unmarshal(raw, &lines); err == nil {
		return cleanSpacing(strings.Join(lines, ""))
	}

	return ""
}
