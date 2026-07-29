package lock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type fakeLockFile struct {
	writeCalls int
	lastInfo   LockInfo
	writeErr   error
}

func (f *fakeLockFile) tryLock() error   { return nil }
func (f *fakeLockFile) unlock() error    { return nil }
func (f *fakeLockFile) close() error     { return nil }
func (f *fakeLockFile) clearInfo() error { return nil }
func (f *fakeLockFile) fdInt() int       { return 0 }
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

func TestLockPathForDB_PreservesDefaultLocation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, ".index", "quant.db")
	got, err := LockPathForDB(dbPath)
	if err != nil {
		t.Fatalf("LockPathForDB() error = %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if want := LockPath(canonicalRoot); got != want {
		t.Fatalf("LockPathForDB() = %q, want %q", got, want)
	}
}

func TestTryAcquireForDB_SeparatesDatabases(t *testing.T) {
	dir := t.TempDir()
	first, err := TryAcquireForDB(filepath.Join(dir, "first.db"), "first", "")
	if err != nil {
		t.Fatalf("first TryAcquireForDB() error = %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := TryAcquireForDB(filepath.Join(dir, "second.db"), "second", "")
	if err != nil {
		t.Fatalf("second TryAcquireForDB() error = %v", err)
	}
	defer func() { _ = second.Release() }()
}

func TestTryAcquireForDBWithFingerprintRecordsIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quant.db")
	lk, err := TryAcquireForDBWithFingerprint(dbPath, "instance", "127.0.0.1:9000", "fingerprint")
	if err != nil {
		t.Fatalf("TryAcquireForDBWithFingerprint() error = %v", err)
	}
	defer func() { _ = lk.Release() }()
	if got := lk.Info().ConfigFingerprint; got != "fingerprint" {
		t.Fatalf("ConfigFingerprint = %q, want %q", got, "fingerprint")
	}
}

func TestTryAcquireForDB_CanonicalizesSymlinkAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	targetLock, err := LockPathForDB(target)
	if err != nil {
		t.Fatalf("target LockPathForDB() error = %v", err)
	}
	aliasLock, err := LockPathForDB(alias)
	if err != nil {
		t.Fatalf("alias LockPathForDB() error = %v", err)
	}
	if targetLock != aliasLock {
		t.Fatalf("lock paths differ: target=%q alias=%q", targetLock, aliasLock)
	}

	lk, err := TryAcquireForDB(target, "target", "")
	if err != nil {
		t.Fatalf("TryAcquireForDB(target) error = %v", err)
	}
	defer func() { _ = lk.Release() }()
	if _, err := TryAcquireForDB(alias, "alias", ""); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquireForDB(alias) error = %v, want ErrLockHeld", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("Remove(target) error = %v", err)
	}
	danglingAliasLock, err := LockPathForDB(alias)
	if err != nil {
		t.Fatalf("dangling alias LockPathForDB() error = %v", err)
	}
	if danglingAliasLock != targetLock {
		t.Fatalf("dangling alias changed lock path: got=%q want=%q", danglingAliasLock, targetLock)
	}
}

func TestLockPathForDB_CanonicalizesDefaultDatabaseAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(realRoot, ".index"), 0750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	dbPath := filepath.Join(realRoot, ".index", "quant.db")
	if err := os.WriteFile(dbPath, nil, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	aliasRoot := filepath.Join(base, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	realLock, err := LockPathForDB(dbPath)
	if err != nil {
		t.Fatalf("real LockPathForDB() error = %v", err)
	}
	aliasLock, err := LockPathForDB(filepath.Join(aliasRoot, ".index", "quant.db"))
	if err != nil {
		t.Fatalf("alias LockPathForDB() error = %v", err)
	}
	if realLock != aliasLock {
		t.Fatalf("default lock paths differ: real=%q alias=%q", realLock, aliasLock)
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

	lock.UpdateProxyAddr("127.0.0.1:9001", "token")
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
	stat, err := os.Stat(LockPath(dir))
	if err != nil {
		t.Fatalf("lock file stat error = %v", err)
	}
	if stat.Size() != 0 {
		t.Fatalf("lock file size = %d, want 0", stat.Size())
	}
	if CheckMainAlive(dir) {
		t.Fatal("CheckMainAlive() after release = true, want false")
	}
}

func TestTryAcquireTightensExistingLockFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix file modes")
	}

	dir := t.TempDir()
	lockPath := LockPath(dir)
	if err := os.MkdirAll(filepath.Dir(lockPath), stateDirMode); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	lk, err := TryAcquire(dir, "instance", "")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	defer func() { _ = lk.Release() }()

	// Restore the legacy mode after acquisition to verify the token update
	// itself tightens permissions before writing.
	if err := os.Chmod(lockPath, 0644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	lk.UpdateProxyAddr("127.0.0.1:9001", "secret-token")

	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != lockFileMode {
		t.Fatalf("lock file mode = %04o, want %04o", got, lockFileMode)
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

	lock.UpdateProxyAddr("new", "token")

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

	lock.UpdateProxyAddr("new", "token")

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
