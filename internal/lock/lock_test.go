package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeLockFile struct {
	writeCalls int
	lastInfo   LockInfo
	writeErr   error
}

func (f *fakeLockFile) tryLock() error { return nil }
func (f *fakeLockFile) unlock() error  { return nil }
func (f *fakeLockFile) close() error   { return nil }
func (f *fakeLockFile) fdInt() int     { return 0 }
func (f *fakeLockFile) writeInfo(info LockInfo) error {
	f.writeCalls++
	f.lastInfo = info
	return f.writeErr
}

func TestLockPath(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("tmp", "project")
	want := filepath.Join(dir, ".index", lockFileName)
	if got := LockPath(dir); got != want {
		t.Fatalf("LockPath(%q) = %q, want %q", dir, got, want)
	}
}

func TestTryAcquireReadUpdateAndRelease(t *testing.T) {
	dir := t.TempDir()

	lock, err := TryAcquire(dir, "instance-1", "127.0.0.1:9000")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}

	info := lock.Info()
	if info.InstanceID != "instance-1" {
		t.Fatalf("Info().InstanceID = %q, want %q", info.InstanceID, "instance-1")
	}
	if info.ProxyAddr != "127.0.0.1:9000" {
		t.Fatalf("Info().ProxyAddr = %q, want %q", info.ProxyAddr, "127.0.0.1:9000")
	}
	if info.PID != os.Getpid() {
		t.Fatalf("Info().PID = %d, want %d", info.PID, os.Getpid())
	}
	if got := lock.ProxyAddr(); got != "127.0.0.1:9000" {
		t.Fatalf("ProxyAddr() = %q, want %q", got, "127.0.0.1:9000")
	}

	readInfo, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock() error = %v", err)
	}
	if *readInfo != info {
		t.Fatalf("ReadLock() = %+v, want %+v", *readInfo, info)
	}
	if !CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() = false, want true")
	}

	lock.UpdateProxyAddr("127.0.0.1:9001")
	if got := lock.ProxyAddr(); got != "127.0.0.1:9001" {
		t.Fatalf("ProxyAddr() after update = %q, want %q", got, "127.0.0.1:9001")
	}

	readInfo, err = ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock() after update error = %v", err)
	}
	if readInfo.ProxyAddr != "127.0.0.1:9001" {
		t.Fatalf("ReadLock().ProxyAddr after update = %q, want %q", readInfo.ProxyAddr, "127.0.0.1:9001")
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release() error = %v, want nil", err)
	}
	if _, err := os.Stat(LockPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file stat error = %v, want not-exist", err)
	}
	if CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() after release = true, want false")
	}
}

func TestReadLockCorrupted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := LockPath(dir)
	if err := os.MkdirAll(filepath.Dir(lockPath), stateDirMode); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("{not-json"), lockFileMode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ReadLock(dir)
	if !errors.Is(err, ErrLockCorrupted) {
		t.Fatalf("ReadLock() error = %v, want %v", err, ErrLockCorrupted)
	}
	if CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() with corrupted lock = true, want false")
	}
}

func TestUpdateProxyAddrSkipsReleasedLocks(t *testing.T) {
	t.Parallel()

	fake := &fakeLockFile{}
	lock := &Lock{
		info: LockInfo{
			InstanceID: "instance-1",
			PID:        123,
			ProxyAddr:  "old",
		},
		lf:   fake,
		held: false,
	}

	lock.UpdateProxyAddr("new")

	if got := lock.ProxyAddr(); got != "old" {
		t.Fatalf("ProxyAddr() = %q, want %q", got, "old")
	}
	if fake.writeCalls != 0 {
		t.Fatalf("writeCalls = %d, want 0", fake.writeCalls)
	}
}

func TestUpdateProxyAddrKeepsInMemoryStateWhenWriteFails(t *testing.T) {
	t.Parallel()

	fake := &fakeLockFile{writeErr: errors.New("disk full")}
	lock := &Lock{
		info: LockInfo{
			InstanceID: "instance-1",
			PID:        123,
			ProxyAddr:  "old",
		},
		lf:   fake,
		held: true,
	}

	lock.UpdateProxyAddr("new")

	if got := lock.ProxyAddr(); got != "new" {
		t.Fatalf("ProxyAddr() = %q, want %q", got, "new")
	}
	if fake.writeCalls != 1 {
		t.Fatalf("writeCalls = %d, want 1", fake.writeCalls)
	}
	if fake.lastInfo.ProxyAddr != "new" {
		t.Fatalf("last written ProxyAddr = %q, want %q", fake.lastInfo.ProxyAddr, "new")
	}
}

func TestReadLockMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := ReadLock(dir)
	if err == nil {
		t.Fatal("ReadLock() error = nil, want error")
	}
}
