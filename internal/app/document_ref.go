package app

import (
	"path/filepath"
	"strings"
)

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
