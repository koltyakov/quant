package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/koltyakov/quant/internal/logx"
)

const (
	lockFileName = "quant.lock"
	lockFileMode = 0644
	stateDirMode = 0750
)

var (
	ErrLockHeld      = errors.New("lock held by another process")
	ErrLockCorrupted = errors.New("lock file corrupted")
)

type LockInfo struct {
	InstanceID string `json:"instance_id"`
	PID        int    `json:"pid"`
	ProxyAddr  string `json:"proxy_addr"`
}

type Lock struct {
	dir        string
	lockPath   string
	instanceID string
	info       LockInfo

	mu   sync.Mutex
	lf   lockFile
	held bool
}

func LockPath(dir string) string {
	return filepath.Join(dir, ".index", lockFileName)
}

func TryAcquire(dir, instanceID, proxyAddr string) (*Lock, error) {
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	lf, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}

	if err := lf.tryLock(); err != nil {
		existing, readErr := readLockInfo(lockPath)
		_ = lf.close()
		if readErr != nil || isStale(existing) {
			return stealLock(lockPath, dir, instanceID, proxyAddr)
		}
		return nil, ErrLockHeld
	}

	info := LockInfo{
		InstanceID: instanceID,
		PID:        os.Getpid(),
		ProxyAddr:  proxyAddr,
	}

	if err := lf.writeInfo(info); err != nil {
		_ = lf.unlock()
		_ = lf.close()
		return nil, fmt.Errorf("writing lock file: %w", err)
	}

	l := &Lock{
		dir:        dir,
		lockPath:   lockPath,
		instanceID: instanceID,
		info:       info,
		lf:         lf,
		held:       true,
	}

	logx.Info("acquired main lock", "instance", instanceID, "pid", os.Getpid(), "proxy", proxyAddr)
	return l, nil
}

func stealLock(lockPath, dir, instanceID, proxyAddr string) (*Lock, error) {
	_ = os.Remove(lockPath)
	lf, err := openLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("opening lock file for steal: %w", err)
	}

	if err := lf.tryLock(); err != nil {
		_ = lf.close()
		return nil, ErrLockHeld
	}

	info := LockInfo{
		InstanceID: instanceID,
		PID:        os.Getpid(),
		ProxyAddr:  proxyAddr,
	}

	if err := lf.writeInfo(info); err != nil {
		_ = lf.unlock()
		_ = lf.close()
		return nil, fmt.Errorf("writing lock file: %w", err)
	}

	l := &Lock{
		dir:        dir,
		lockPath:   lockPath,
		instanceID: instanceID,
		info:       info,
		lf:         lf,
		held:       true,
	}

	logx.Info("stole stale lock", "instance", instanceID, "pid", os.Getpid(), "proxy", proxyAddr)
	return l, nil
}

func ReadLock(dir string) (*LockInfo, error) {
	lockPath := LockPath(dir)
	info, err := readLockInfo(lockPath)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func CheckMainAlive(dir string) bool {
	lockPath := LockPath(dir)
	info, err := readLockInfo(lockPath)
	if err != nil {
		return false
	}
	return isProcessAlive(info.PID)
}

func (l *Lock) UpdateProxyAddr(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return
	}
	l.info.ProxyAddr = addr
	if err := l.lf.writeInfo(l.info); err != nil {
		logx.Warn("updating proxy addr in lock failed", "err", err)
	}
	logx.Info("updated proxy address in lock", "addr", addr)
}

func (l *Lock) Info() LockInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.info
}

func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return nil
	}
	l.held = false

	_ = l.lf.unlock()
	_ = l.lf.close()
	_ = os.Remove(l.lockPath)

	logx.Info("released main lock", "instance", l.instanceID)
	return nil
}

func (l *Lock) ProxyAddr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.info.ProxyAddr
}

func isStale(info LockInfo) bool {
	return !isProcessAlive(info.PID)
}

func readLockInfo(path string) (LockInfo, error) {
	data, err := os.ReadFile(path) //nolint:gosec // lock file path is constructed from known directory structure
	if err != nil {
		return LockInfo{}, fmt.Errorf("reading lock file: %w", err)
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return LockInfo{}, ErrLockCorrupted
	}
	return info, nil
}
