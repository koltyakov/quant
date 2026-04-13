//go:build !windows

package lock

import (
	"encoding/json"
	"fmt"
	"syscall"
)

type unixLockFile struct {
	fd int
}

func openLockFile(path string) (lockFile, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR, lockFileMode)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	return &unixLockFile{fd: fd}, nil
}

func (l *unixLockFile) tryLock() error {
	return syscall.Flock(l.fd, syscall.LOCK_EX|syscall.LOCK_NB)
}

func (l *unixLockFile) unlock() error {
	return syscall.Flock(l.fd, syscall.LOCK_UN)
}

func (l *unixLockFile) writeInfo(info LockInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if _, err := syscall.Seek(l.fd, 0, 0); err != nil {
		return err
	}
	if err := syscall.Ftruncate(l.fd, 0); err != nil {
		return err
	}
	if _, err := syscall.Write(l.fd, data); err != nil {
		return err
	}
	return nil
}

func (l *unixLockFile) close() error {
	return syscall.Close(l.fd)
}

func (l *unixLockFile) fdInt() int {
	return l.fd
}
