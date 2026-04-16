package app

import "path/filepath"

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
		AbsPath: filepath.Join(root, key),
	}, nil
}

func ResolveStoredDocumentRef(root, storedPath string) (DocumentRef, error) {
	return ResolveDocumentRef(root, filepath.Join(root, storedPath))
}

func ResolveDocumentRefFromKey(root, key string) (DocumentRef, error) {
	return ResolveDocumentRef(root, filepath.Join(root, key))
}
