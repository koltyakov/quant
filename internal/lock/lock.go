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
	lockFileMode = 0600
	stateDirMode = 0750
)

var (
	ErrLockHeld      = errors.New("lock held by another process")
	ErrLockCorrupted = errors.New("lock file corrupted")
)

type LockInfo struct {
	InstanceID        string `json:"instance_id"`
	PID               int    `json:"pid"`
	ProxyAddr         string `json:"proxy_addr"`
	ProxyToken        string `json:"proxy_token,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
}

type Lock struct {
	instanceID string
	info       LockInfo

	mu   sync.Mutex
	lf   lockFile
	held bool
}

func LockPath(dir string) string {
	return filepath.Join(dir, ".index", lockFileName)
}

// LockPathForDB returns the lock protecting a canonical database path. A
// non-symlinked default database keeps its historical lock location.
func LockPathForDB(dbPath string) (string, error) {
	canonical, err := canonicalDBPath(dbPath)
	if err != nil {
		return "", err
	}
	if filepath.Base(canonical) == "quant.db" && filepath.Base(filepath.Dir(canonical)) == ".index" {
		return filepath.Join(filepath.Dir(canonical), lockFileName), nil
	}
	return canonical + ".lock", nil
}

func canonicalDBPath(dbPath string) (string, error) {
	return canonicalDBPathDepth(dbPath, 0)
}

func canonicalDBPathDepth(dbPath string, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("resolving database symlinks: too many links")
	}
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolving database path: %w", err)
	}
	absPath = filepath.Clean(absPath)
	if info, err := os.Lstat(absPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absPath)
		if err != nil {
			return "", fmt.Errorf("reading database symlink: %w", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(absPath), target)
		}
		return canonicalDBPathDepth(target, depth+1)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspecting database symlink: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return filepath.Clean(resolved), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolving database symlinks: %w", err)
	}

	current := absPath
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return absPath, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolving database parent symlinks: %w", err)
		}
	}
}

func TryAcquire(dir, instanceID, proxyAddr string) (*Lock, error) {
	return tryAcquirePath(LockPath(dir), instanceID, proxyAddr, "")
}

func TryAcquireForDB(dbPath, instanceID, proxyAddr string) (*Lock, error) {
	return TryAcquireForDBWithFingerprint(dbPath, instanceID, proxyAddr, "")
}

// TryAcquireForDBWithFingerprint acquires database ownership and records the
// indexing configuration served by the main process.
func TryAcquireForDBWithFingerprint(dbPath, instanceID, proxyAddr, fingerprint string) (*Lock, error) {
	lockPath, err := LockPathForDB(dbPath)
	if err != nil {
		return nil, err
	}
	return tryAcquirePath(lockPath, instanceID, proxyAddr, fingerprint)
}

func tryAcquirePath(lockPath, instanceID, proxyAddr, fingerprint string) (*Lock, error) {
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	lf, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}

	if err := lf.tryLock(); err != nil {
		_ = lf.close()
		return nil, ErrLockHeld
	}

	info := LockInfo{
		InstanceID:        instanceID,
		PID:               os.Getpid(),
		ProxyAddr:         proxyAddr,
		ConfigFingerprint: fingerprint,
	}

	if err := lf.writeInfo(info); err != nil {
		_ = lf.unlock()
		_ = lf.close()
		return nil, fmt.Errorf("writing lock file: %w", err)
	}

	l := &Lock{
		instanceID: instanceID,
		info:       info,
		lf:         lf,
		held:       true,
	}

	logx.Info("acquired main lock", "instance", instanceID, "pid", os.Getpid(), "proxy", proxyAddr)
	return l, nil
}

func ReadLock(dir string) (*LockInfo, error) {
	return readLockPath(LockPath(dir))
}

func ReadLockForDB(dbPath string) (*LockInfo, error) {
	lockPath, err := LockPathForDB(dbPath)
	if err != nil {
		return nil, err
	}
	return readLockPath(lockPath)
}

func readLockPath(lockPath string) (*LockInfo, error) {
	info, err := readLockInfo(lockPath)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func CheckMainAlive(dir string) bool {
	return checkMainAlivePath(LockPath(dir))
}

func CheckMainAliveForDB(dbPath string) bool {
	lockPath, err := LockPathForDB(dbPath)
	if err != nil {
		return false
	}
	return checkMainAlivePath(lockPath)
}

func checkMainAlivePath(lockPath string) bool {
	info, err := readLockInfo(lockPath)
	if err != nil {
		return false
	}
	return isProcessAlive(info.PID)
}

func (l *Lock) UpdateProxyAddr(addr, token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return
	}
	l.info.ProxyAddr = addr
	l.info.ProxyToken = token
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

	_ = l.lf.clearInfo()
	_ = l.lf.unlock()
	_ = l.lf.close()

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
