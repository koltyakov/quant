package lock

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHelperLockHolder(t *testing.T) {
	if os.Getenv("LOCK_HELPER") != "hold" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("LOCK_DIR")
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}
	lf, err := openLockFile(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	if err := lf.tryLock(); err != nil {
		fmt.Fprintf(os.Stderr, "lock: %v\n", err)
		os.Exit(1)
	}
	info := LockInfo{InstanceID: "holder", PID: os.Getpid(), ProxyAddr: "holder:9999"}
	if err := lf.writeInfo(info); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("READY")
	select {}
}

func TestTryAcquire_Success(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lk, err := TryAcquire(dir, "test-instance", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	info := lk.Info()
	if info.InstanceID != "test-instance" {
		t.Fatalf("Info().InstanceID = %q, want %q", info.InstanceID, "test-instance")
	}
}

func TestTryAcquire_HeldByAnotherProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLockHolder$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER=hold", "LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("helper did not produce output")
	}

	_, err = TryAcquire(dir, "competing", "competing:9000")
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquire() error = %v, want ErrLockHeld", err)
	}
}

func TestStealLock_StaleLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	staleInfo := LockInfo{InstanceID: "dead", PID: 9999999, ProxyAddr: "dead:9000"}
	data, err := json.Marshal(staleInfo)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(lockPath, data, lockFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lk, err := stealLock(lockPath, dir, "fresh", "fresh:9000")
	if err != nil {
		t.Fatalf("stealLock() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	info := lk.Info()
	if info.InstanceID != "fresh" {
		t.Fatalf("Info().InstanceID = %q, want %q", info.InstanceID, "fresh")
	}
}

func TestReadLockInfo_ValidFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	want := LockInfo{InstanceID: "test-inst", PID: 12345, ProxyAddr: "127.0.0.1:8080"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(lockPath, data, lockFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := readLockInfo(lockPath)
	if err != nil {
		t.Fatalf("readLockInfo() error = %v", err)
	}
	if got != want {
		t.Fatalf("readLockInfo() = %+v, want %+v", got, want)
	}
}

func TestReadLockInfo_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := readLockInfo(filepath.Join(t.TempDir(), "nonexistent", "lock"))
	if err == nil {
		t.Fatal("readLockInfo() error = nil, want error")
	}
}

func TestReadLockInfo_Corrupted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("not json"), lockFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readLockInfo(lockPath)
	if !errors.Is(err, ErrLockCorrupted) {
		t.Fatalf("readLockInfo() error = %v, want ErrLockCorrupted", err)
	}
}

func TestIsStale_NotAliveProcess(t *testing.T) {
	t.Parallel()

	if !isStale(LockInfo{PID: 9999999}) {
		t.Fatal("isStale() = false for dead PID, want true")
	}
}

func TestIsStale_AliveProcess(t *testing.T) {
	t.Parallel()

	if isStale(LockInfo{PID: os.Getpid()}) {
		t.Fatal("isStale() = true for current process, want false")
	}
}

func TestLockPath_ReturnsPath(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("tmp", "project")
	want := filepath.Join(dir, ".index", lockFileName)
	if got := LockPath(dir); got != want {
		t.Fatalf("LockPath(%q) = %q, want %q", dir, got, want)
	}
}

func TestLock_Release(t *testing.T) {
	dir := t.TempDir()

	lk, err := TryAcquire(dir, "release-test", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}

	if err := lk.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	if _, err := os.Stat(LockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock file still exists after release")
	}

	if err := lk.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
}

func TestLock_UpdateProxyAddr(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lk, err := TryAcquire(dir, "proxy-test", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	lk.UpdateProxyAddr("127.0.0.1:9001")

	if got := lk.ProxyAddr(); got != "127.0.0.1:9001" {
		t.Fatalf("ProxyAddr() = %q, want %q", got, "127.0.0.1:9001")
	}

	info, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock() error = %v", err)
	}
	if info.ProxyAddr != "127.0.0.1:9001" {
		t.Fatalf("ReadLock().ProxyAddr = %q, want %q", info.ProxyAddr, "127.0.0.1:9001")
	}
}

func TestLock_Info(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lk, err := TryAcquire(dir, "info-test", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	info := lk.Info()
	if info.InstanceID != "info-test" {
		t.Fatalf("Info().InstanceID = %q, want %q", info.InstanceID, "info-test")
	}
	if info.PID != os.Getpid() {
		t.Fatalf("Info().PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.ProxyAddr != "127.0.0.1:8080" {
		t.Fatalf("Info().ProxyAddr = %q, want %q", info.ProxyAddr, "127.0.0.1:8080")
	}
}

func TestLock_ProxyAddr(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lk, err := TryAcquire(dir, "addr-test", "127.0.0.1:7070")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	if got := lk.ProxyAddr(); got != "127.0.0.1:7070" {
		t.Fatalf("ProxyAddr() = %q, want %q", got, "127.0.0.1:7070")
	}
}

func TestCheckMainAlive_WithLock(t *testing.T) {
	dir := t.TempDir()

	lk, err := TryAcquire(dir, "alive-test", "127.0.0.1:9999")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}

	if !CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() = false, want true")
	}

	_ = lk.Release()

	if CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() after release = true, want false")
	}
}

func TestReadLock_FromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lk, err := TryAcquire(dir, "readlock-test", "127.0.0.1:5050")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	info, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock() error = %v", err)
	}
	if info.InstanceID != "readlock-test" {
		t.Fatalf("ReadLock().InstanceID = %q, want %q", info.InstanceID, "readlock-test")
	}
}

func TestFdInt_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	lf, err := openLockFile(lockPath)
	if err != nil {
		t.Fatalf("openLockFile() error = %v", err)
	}
	t.Cleanup(func() { _ = lf.close() })

	fd := lf.fdInt()
	if fd <= 0 {
		t.Fatalf("fdInt() = %d, want positive file descriptor", fd)
	}
}
