package extract

import (
	"context"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

type PDFExtractor struct{}

func (p *PDFExtractor) Extract(_ context.Context, path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var parts []string
	pageCount := r.NumPage()
	for i := 1; i <= pageCount; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		content, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(strings.Join([]string{
			"[Page " + strconv.Itoa(i) + "]",
			content,
		}, "\n")))
	}

	return strings.Join(parts, "\n\n"), nil
}

func (p *PDFExtractor) Supports(path string) bool {
	return strings.EqualFold(ext(path), ".pdf")
}
