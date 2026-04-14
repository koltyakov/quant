package extract

import (
	"context"
	"io"
	"os"
	"strings"

	"golang.org/x/net/html"
)

type HTMLExtractor struct{}

func (h *HTMLExtractor) Extract(ctx context.Context, path string) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}

	f, err := os.Open(path) //nolint:gosec // Extractor intentionally opens user-selected local files for indexing.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	if err := ensureFileSize(path, maxExtractorFileSize); err != nil {
		return "", err
	}

	return extractHTMLText(ctx, f)
}

func (h *HTMLExtractor) Supports(path string) bool {
	e := strings.ToLower(ext(path))
	return e == ".html" || e == ".htm"
}

func extractHTMLText(ctx context.Context, r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	atLineStart := true

	writeText := func(text string) {
		if text == "" {
			return
		}
		if !atLineStart && buf.Len() > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(text)
		atLineStart = false
	}

	writeLineBreak := func() {
		if buf.Len() > 0 {
			buf.WriteByte('\n')
			atLineStart = true
		}
	}

	writeCellSeparator := func() {
		if !atLineStart {
			buf.WriteString(" | ")
		}
		atLineStart = true
	}

	var f func(*html.Node)
	f = func(n *html.Node) {
		if err := ctx.Err(); err != nil {
			return
		}

		if n.Type == html.TextNode {
			writeText(strings.TrimSpace(n.Data))
		}

		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript":
				return
			case "br":
				buf.WriteByte('\n')
				atLineStart = true
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
				"li", "tr", "blockquote", "section", "article",
				"header", "footer", "nav", "aside", "main",
				"figure", "figcaption", "details", "summary",
				"dd", "dt", "dl", "hr", "pre", "address":
				writeLineBreak()
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if n.Type == html.ElementNode && (n.Data == "td" || n.Data == "th") && c == n.FirstChild {
				writeCellSeparator()
			}
			f(c)
		}

		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
				"li", "tr", "blockquote", "section", "article",
				"header", "footer", "nav", "aside", "main",
				"figure", "figcaption", "details", "summary",
				"dd", "dt", "dl", "hr", "pre", "address":
				writeLineBreak()
			case "title":
				writeLineBreak()
			}
		}
	}

	f(doc)

	return cleanHTMLText(buf.String()), nil
}

func cleanHTMLText(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))

	prevNewline := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !prevNewline {
				b.WriteByte('\n')
				prevNewline = true
			}
			continue
		}
		prevNewline = false
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(trimmed)
	}

	return strings.TrimSpace(b.String())
}
