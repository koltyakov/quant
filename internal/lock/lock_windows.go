//go:build windows

package lock

import (
	"encoding/json"
	"fmt"

	"golang.org/x/sys/windows"
)

type windowsLockFile struct {
	handle windows.Handle
}

func openLockFile(path string) (lockFile, error) {
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	return &windowsLockFile{handle: h}, nil
}

func (l *windowsLockFile) tryLock() error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(l.handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		return fmt.Errorf("lock file held: %w", err)
	}
	return nil
}

func (l *windowsLockFile) unlock() error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(l.handle, 0, 1, 0, ol)
}

func (l *windowsLockFile) writeInfo(info LockInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	var overlapped windows.Overlapped
	overlapped.Offset = 0
	var written uint32
	if err := windows.WriteFile(l.handle, data, &written, &overlapped); err != nil {
		return err
	}
	return windows.SetEndOfFile(l.handle)
}

func (l *windowsLockFile) close() error {
	return windows.CloseHandle(l.handle)
}

func (l *windowsLockFile) fdInt() int {
	return int(l.handle)
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}
