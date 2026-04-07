package extract

import "context"

type Extractor interface {
	Extract(ctx context.Context, path string) (string, error)
	Supports(path string) bool
}

type Router struct {
	extractors []Extractor
}

func NewRouter() *Router {
	return &Router{
		extractors: []Extractor{
			&TextExtractor{},
			&NotebookExtractor{},
			&PDFExtractor{},
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
