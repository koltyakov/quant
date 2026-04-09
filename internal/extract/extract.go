package extract

import "context"

type Extractor interface {
	Extract(ctx context.Context, path string) (string, error)
	Supports(path string) bool
}

type Options struct {
	PDFOCRLang string
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
			&NotebookExtractor{},
			&PDFExtractor{ocrLanguages: cfg.PDFOCRLang},
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
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	return ""
}

func basename(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
