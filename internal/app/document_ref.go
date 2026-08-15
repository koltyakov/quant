package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errUnsafeDocumentPath = errors.New("document path is a symlink or resolves outside the watch root")

// DocumentRef carries the canonical document key used in the index together
// with the absolute filesystem path used for I/O.
type DocumentRef struct {
	Key     string
	AbsPath string
}

func ResolveDocumentRef(root, path string) (DocumentRef, error) {
	key, err := DocumentKey(root, path)
	if err != nil {
		return DocumentRef{}, err
	}
	return DocumentRef{
		Key:     key,
		AbsPath: filepath.Join(root, filepath.FromSlash(key)),
	}, nil
}

func ResolveStoredDocumentRef(root, storedPath string) (DocumentRef, error) {
	return ResolveDocumentRef(root, filepath.Join(root, storedDocumentPath(storedPath)))
}

func ResolveDocumentRefFromKey(root, key string) (DocumentRef, error) {
	return ResolveDocumentRef(root, filepath.Join(root, storedDocumentPath(key)))
}

func storedDocumentPath(key string) string {
	return filepath.FromSlash(strings.ReplaceAll(key, `\`, "/"))
}

// statDocumentPath rejects direct symlinks and paths whose parent components
// resolve outside root. The returned metadata comes from Lstat so callers do
// not accidentally treat a symlink target as the indexed document.
func statDocumentPath(root, path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", errUnsafeDocumentPath, path)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolving watch root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if _, err := DocumentKey(resolvedRoot, resolvedPath); err != nil {
		return nil, fmt.Errorf("%w: %s", errUnsafeDocumentPath, path)
	}
	return info, nil
}
