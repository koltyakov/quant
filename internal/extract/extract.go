package extract

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	qerrors "github.com/koltyakov/quant/internal/errors"
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

func (r *Router) Extract(ctx context.Context, path string) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			text = ""
			err = fmt.Errorf("%w: %s: extractor panic: %v", qerrors.ErrExtractionFailed, filepath.Base(path), rec)
		}
	}()
	for _, e := range r.extractors {
		if e.Supports(path) {
			text, err := e.Extract(ctx, path)
			if err != nil {
				return "", err
			}
			if len(text) > maxExtractedTextBytes {
				return "", fmt.Errorf("%w: extracted text exceeds %s", ErrFileTooLarge, formatExtractBytes(maxExtractedTextBytes))
			}
			return text, nil
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
