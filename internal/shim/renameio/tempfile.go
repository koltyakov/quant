package renameio

import (
	"os"
	"path/filepath"
)

// PendingFile is a temporary file that can replace a destination path.
type PendingFile struct {
	*os.File
	path string
}

// TempFile creates a temporary file in the destination directory so a later
// rename stays on the same filesystem across platforms.
func TempFile(dir, path string) (*PendingFile, error) {
	if dir == "" {
		dir = filepath.Dir(path)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, err
	}

	return &PendingFile{
		File: f,
		path: path,
	}, nil
}

// Cleanup closes and removes the temporary file when it still exists.
func (t *PendingFile) Cleanup() {
	if t == nil || t.File == nil {
		return
	}
	name := t.Name()
	_ = t.File.Close()
	_ = os.Remove(name)
}

// CloseAtomicallyReplace closes the temporary file and moves it into place.
func (t *PendingFile) CloseAtomicallyReplace() error {
	if t == nil || t.File == nil {
		return nil
	}

	name := t.Name()
	if err := t.File.Close(); err != nil {
		return err
	}
	t.File = nil

	if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, t.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
