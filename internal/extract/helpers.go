package extract

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
)

const maxExtractorFileSize int64 = 64 << 20

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func ensureFileSize(path string, limit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return fmt.Errorf("file exceeds extraction size limit: %s (%s > %s)", path, formatExtractBytes(info.Size()), formatExtractBytes(limit))
	}
	return nil
}

func readFileLimited(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := ensureFileSize(path, limit); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return readAllLimited(ctx, f, limit, path)
}

func readZipFile(ctx context.Context, files []*zip.File, name string) ([]byte, error) {
	for _, f := range files {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > uint64(maxExtractorFileSize) {
			return nil, fmt.Errorf("zip entry exceeds extraction size limit: %s (%s > %s)", name, formatExtractBytes(int64(f.UncompressedSize64)), formatExtractBytes(maxExtractorFileSize))
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()

		return readAllLimited(ctx, rc, maxExtractorFileSize, name)
	}
	return nil, fmt.Errorf("zip entry not found: %s", name)
}

func readAllLimited(ctx context.Context, r io.Reader, limit int64, name string) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}

	buf := make([]byte, 32*1024)
	out := make([]byte, 0, minInt64(limit, 32*1024))
	var total int64

	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}

		n, err := r.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return nil, fmt.Errorf("%s exceeds extraction size limit (%s)", name, formatExtractBytes(limit))
			}
			out = append(out, buf[:n]...)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func nextXMLToken(ctx context.Context, decoder *xml.Decoder) (xml.Token, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return decoder.Token()
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func formatExtractBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
