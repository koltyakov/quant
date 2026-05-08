package extract

import (
	"context"
	"path/filepath"
	"time"
)

// Extractor extracts plain text from a file for indexing.
type Extractor interface {
	Extract(ctx context.Context, path string) (string, error)
	Supports(path string) bool
}

// Options configures optional extraction behavior such as PDF OCR.
type Options struct {
	PDFOCRLang    string
	PDFOCRTimeout time.Duration
}

// Router dispatches extraction to the first Extractor that supports a given file path.
type Router struct {
	extractors []Extractor
}

// NewRouter creates a Router with all built-in extractors (text, HTML, PDF, OOXML, ODF, RTF, notebook).
func NewRouter(opts ...Options) *Router {
	var cfg Options
	if len(opts) > 0 {
		cfg = opts[0]
	}

	return &Router{
		extractors: []Extractor{
			&TextExtractor{},
			&HTMLExtractor{},
			&NotebookExtractor{},
			&PDFExtractor{ocrLanguages: cfg.PDFOCRLang, ocrTimeout: cfg.PDFOCRTimeout},
			&OOXMLExtractor{},
			&ODFExtractor{},
			&RTFExtractor{},
		},
	}
}

func (r *Router) Extract(ctx context.Context, path string) (string, error) {
	for _, e := range r.extractors {
		if e.Supports(path) {
			return e.Extract(ctx, path)
		}
	}
	return "", nil
}

func (r *Router) Supports(path string) bool {
	for _, e := range r.extractors {
		if e.Supports(path) {
			return true
		}
	}
	return false
}

func ext(path string) string {
	return filepath.Ext(path)
}

func basename(path string) string {
	return filepath.Base(path)
}
