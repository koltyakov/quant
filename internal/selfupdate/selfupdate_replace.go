package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type replaceOps struct {
	rename func(oldPath, newPath string) error
	remove func(path string) error
	copy   func(srcPath, dstPath string, mode os.FileMode) error
}

func replaceBinary(newBinary []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determine executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	return replaceBinaryAt(exe, newBinary)
}

func replaceBinaryAt(exe string, newBinary []byte) error {
	exe = filepath.Clean(exe)
	caps := getFileCaps(exe)

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, "quant-update-*")
	if err != nil {
		if isPermissionError(err) {
			return replaceBinaryCopy(exe, newBinary, caps)
		}
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	//nolint:gosec // tmpPath is a temp file adjacent to the resolved current executable.
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		return err
	}

	//nolint:gosec // Destination is the resolved current executable path on the local filesystem.
	if err := os.Rename(tmpPath, exe); err != nil {
		if shouldFallbackToCopy(err) {
			return replaceBinaryCopy(exe, newBinary, caps)
		}
		return fmt.Errorf("rename: %w", err)
	}

	setFileCaps(exe, caps)
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

func replaceBinaryCopy(exe string, newBinary []byte, caps string) error {
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}
	mode := info.Mode()

	tmp, err := os.CreateTemp("", "quant-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return replaceBinaryCopyStaged(tmpPath, exe, mode, caps, replaceOps{
		rename: os.Rename,
		remove: os.Remove,
		copy:   copyFilePath,
	})
}

func replaceBinaryCopyStaged(stagedPath, exe string, mode os.FileMode, caps string, ops replaceOps) error {
	if ops.rename == nil {
		ops.rename = os.Rename
	}
	if ops.remove == nil {
		ops.remove = os.Remove
	}
	if ops.copy == nil {
		ops.copy = copyFilePath
	}

	backupPath := fmt.Sprintf("%s.old.%d", exe, time.Now().UnixNano())
	hasBackup := false
	if err := ops.rename(exe, backupPath); err == nil {
		hasBackup = true
	} else {
		if err := ops.copy(exe, backupPath, mode); err != nil {
			return fmt.Errorf("backup old binary: %w", err)
		}
		hasBackup = true
		if err := ops.remove(exe); err != nil {
			_ = ops.remove(backupPath)
			return fmt.Errorf("remove old binary (the binary must be in a directory writable by the running user): %w", err)
		}
	}

	if err := ops.copy(stagedPath, exe, mode); err != nil {
		if hasBackup {
			_ = ops.remove(exe)
			if restoreErr := ops.rename(backupPath, exe); restoreErr != nil {
				_ = ops.remove(exe)
				if copyRestoreErr := ops.copy(backupPath, exe, mode); copyRestoreErr != nil {
					return fmt.Errorf("create new binary: %w (restore old binary failed: %v; copy restore failed: %v)", err, restoreErr, copyRestoreErr)
				}
			}
		}
		return fmt.Errorf("create new binary: %w", err)
	}
	if hasBackup {
		_ = ops.remove(backupPath)
	}

	setFileCaps(exe, caps)
	if err := syncDir(filepath.Dir(exe)); err != nil {
		return err
	}
	return nil
}

func copyFilePath(srcPath, dstPath string, mode os.FileMode) error {
	//nolint:gosec // Self-update intentionally copies between explicit local filesystem paths.
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	defer func() { _ = src.Close() }()

	//nolint:gosec // Destination path is computed by the updater for the target binary location.
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return nil
}

func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	//nolint:gosec // Syncing a directory requires opening the explicit target directory path.
	dir, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

func getFileCaps(path string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	//nolint:gosec // getcap is invoked with a controlled binary name and explicit file path.
	out, err := exec.Command("getcap", path).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if _, after, ok := strings.Cut(line, " "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func setFileCaps(path, caps string) {
	if caps == "" || runtime.GOOS != "linux" {
		return
	}
	//nolint:gosec // setcap is invoked intentionally to preserve existing Linux file capabilities.
	_ = exec.Command("setcap", caps, path).Run()
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func shouldFallbackToCopy(err error) bool {
	if err == nil {
		return false
	}
	if isPermissionError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "text file busy") ||
		strings.Contains(msg, "cross-device link") ||
		strings.Contains(msg, "device or resource busy")
}
