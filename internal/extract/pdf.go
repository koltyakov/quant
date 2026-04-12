package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/logx"
	"github.com/ledongthuc/pdf"
)

var ErrOCRFailed = errors.New("pdf ocr extraction failed")

const defaultPDFOCRTimeout = 2 * time.Minute

type PDFExtractor struct {
	findOCRBinary func() (string, bool)
	inspectPDF    func(ctx context.Context, path string) (pdfInspection, error)
	runOCR        func(ctx context.Context, binaryPath, path, languages string, timeout time.Duration) (string, error)
	ocrLanguages  string
	ocrTimeout    time.Duration

	ocrLookupOnce sync.Once
	ocrBinaryPath string
	ocrAvailable  bool
}

type pdfInspection struct {
	Text             string
	HasNativeText    bool
	HasIllustrations bool
}

func (p *PDFExtractor) Extract(ctx context.Context, path string) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}

	if err := ensureFileSize(path, maxExtractorFileSize); err != nil {
		return "", err
	}

	inspection, err := p.pdfInspector()(ctx, path)
	if err != nil {
		return "", err
	}
	if inspection.HasNativeText && inspection.HasIllustrations {
		logx.Info("skipping illustrated pdf", "path", path)
		return "", nil
	}
	if inspection.HasNativeText {
		return inspection.Text, nil
	}

	binaryPath, ok := p.ocrBinary()
	if !ok {
		return "", nil
	}

	text, err := p.ocrRunner()(ctx, binaryPath, path, p.languages(), p.timeout())
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		logx.Warn("pdf ocr fallback skipped", "path", path, "err", err)
		return "", ErrOCRFailed
	}

	return strings.TrimSpace(text), nil
}

func (p *PDFExtractor) timeout() time.Duration {
	if p.ocrTimeout > 0 {
		return p.ocrTimeout
	}
	return defaultPDFOCRTimeout
}

func inspectPDF(ctx context.Context, path string) (pdfInspection, error) {
	if err := checkContext(ctx); err != nil {
		return pdfInspection{}, err
	}

	f, r, err := pdf.Open(path)
	if err != nil {
		return pdfInspection{}, err
	}
	defer func() { _ = f.Close() }()

	result := pdfInspection{}
	var parts []string
	pageCount := r.NumPage()
	for i := 1; i <= pageCount; i++ {
		if err := checkContext(ctx); err != nil {
			return pdfInspection{}, err
		}

		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		if pageHasIllustrations(page) {
			result.HasIllustrations = true
		}
		content, err := page.GetPlainText(nil)
		if err != nil {
			logx.Warn("pdf text extraction skipped page", "page", i, "path", path, "err", err)
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		result.HasNativeText = true
		parts = append(parts, strings.TrimSpace(strings.Join([]string{
			"[Page " + strconv.Itoa(i) + "]",
			content,
		}, "\n")))
	}

	result.Text = strings.Join(parts, "\n\n")
	return result, nil
}

func pageHasIllustrations(page pdf.Page) bool {
	xObjects := page.Resources().Key("XObject")
	if xObjects.IsNull() {
		return false
	}
	for _, name := range xObjects.Keys() {
		xObject := xObjects.Key(name)
		if xObject.Key("Subtype").Name() == "Image" {
			return true
		}
	}
	return false
}

func (p *PDFExtractor) pdfInspector() func(ctx context.Context, path string) (pdfInspection, error) {
	if p.inspectPDF != nil {
		return p.inspectPDF
	}
	return inspectPDF
}

func (p *PDFExtractor) ocrBinary() (string, bool) {
	if p.findOCRBinary != nil {
		return p.findOCRBinary()
	}

	p.ocrLookupOnce.Do(func() {
		path, err := exec.LookPath("ocrmypdf")
		if err == nil {
			p.ocrBinaryPath = path
			p.ocrAvailable = true
		}
	})

	return p.ocrBinaryPath, p.ocrAvailable
}

func (p *PDFExtractor) languages() string {
	if strings.TrimSpace(p.ocrLanguages) == "" {
		return "eng"
	}
	return strings.TrimSpace(p.ocrLanguages)
}

func (p *PDFExtractor) ocrRunner() func(ctx context.Context, binaryPath, path, languages string, timeout time.Duration) (string, error) {
	if p.runOCR != nil {
		return p.runOCR
	}
	return runOCRmyPDF
}

func runOCRmyPDF(ctx context.Context, binaryPath, path, languages string, timeout time.Duration) (string, error) {
	tmpDir, err := os.MkdirTemp("", "quant-ocrmypdf-*")
	if err != nil {
		return "", fmt.Errorf("creating OCR temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sidecarPath := filepath.Join(tmpDir, "ocr.txt")

	ocrCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // OCR binary path is discovered from PATH or injected by tests; arguments are fixed.
	cmd := exec.CommandContext(ocrCtx, binaryPath,
		"--skip-text",
		"--rotate-pages",
		"--deskew",
		"-l",
		languages,
		"--sidecar",
		sidecarPath,
		"--output-type=none",
		path,
		"-",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ocrCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("ocrmypdf timed out after %s", timeout)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		msg := strings.TrimSpace(string(output))
		if msg != "" {
			return "", fmt.Errorf("ocrmypdf failed: %s", msg)
		}
		return "", fmt.Errorf("ocrmypdf failed: %w", err)
	}

	//nolint:gosec // Sidecar path is created in a temp directory controlled by this function.
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return "", fmt.Errorf("reading OCR sidecar: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func (p *PDFExtractor) Supports(path string) bool {
	return strings.EqualFold(ext(path), ".pdf")
}
