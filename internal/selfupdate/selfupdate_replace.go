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
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		return err
	}

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

	backupPath := fmt.Sprintf("%s.old.%d", exe, time.Now().UnixNano())
	swappedWithBackup := false
	if err := os.Rename(exe, backupPath); err == nil {
		swappedWithBackup = true
	} else {
		if err := os.Remove(exe); err != nil {
			return fmt.Errorf("remove old binary (the binary must be in a directory writable by the running user): %w", err)
		}
	}

	if err := copyFilePath(tmpPath, exe, mode); err != nil {
		if swappedWithBackup {
			if restoreErr := os.Rename(backupPath, exe); restoreErr != nil {
				return fmt.Errorf("create new binary: %w (restore old binary failed: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("create new binary: %w", err)
	}
	if swappedWithBackup {
		_ = os.Remove(backupPath)
	}

	setFileCaps(exe, caps)
	if err := syncDir(filepath.Dir(exe)); err != nil {
		return err
	}
	return nil
}

func copyFilePath(srcPath, dstPath string, mode os.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	defer func() { _ = src.Close() }()

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
