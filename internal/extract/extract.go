package extract

import (
	"context"
	"path/filepath"
	"time"
)

type Extractor interface {
	Extract(ctx context.Context, path string) (string, error)
	Supports(path string) bool
}

type Options struct {
	PDFOCRLang    string
	PDFOCRTimeout time.Duration
}

type Router struct {
	extractors []Extractor
}

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
