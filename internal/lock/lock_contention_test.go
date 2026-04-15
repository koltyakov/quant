package lock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStealLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	pid := os.Getpid()
	info := LockInfo{InstanceID: "stale", PID: pid, ProxyAddr: "addr"}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(lockPath, data, lockFileMode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	lk, err := stealLock(lockPath, dir, "new-instance", "new-addr")
	if err != nil {
		t.Fatalf("stealLock() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	gotInfo := lk.Info()
	if gotInfo.InstanceID != "new-instance" {
		t.Fatalf("expected instance new-instance, got %q", gotInfo.InstanceID)
	}
	if gotInfo.PID != pid {
		t.Fatalf("expected pid %d, got %d", pid, gotInfo.PID)
	}
}

func TestIsStale_DeadPID(t *testing.T) {
	deadPID := 9999999
	if !isStale(LockInfo{PID: deadPID}) {
		t.Fatal("expected stale for dead PID")
	}
}

func TestIsStale_CurrentProcess(t *testing.T) {
	currentPID := os.Getpid()
	if isStale(LockInfo{PID: currentPID}) {
		t.Fatal("expected not stale for current process PID")
	}
}

func TestIsStale_ZeroPID(t *testing.T) {
	if !isStale(LockInfo{PID: 0}) {
		t.Fatal("expected stale for PID 0")
	}
	if !isStale(LockInfo{PID: -1}) {
		t.Fatal("expected stale for negative PID")
	}
}

func TestTryAcquire_StealsStaleLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lock stealing not supported on windows")
	}

	dir := t.TempDir()
	lockPath := LockPath(dir)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, stateDirMode); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	staleInfo := LockInfo{InstanceID: "dead-instance", PID: 9999999, ProxyAddr: "dead:9000"}
	data, err := json.Marshal(staleInfo)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(lockPath, data, lockFileMode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	lk, err := TryAcquire(dir, "fresh-instance", "fresh:9000")
	if err != nil {
		t.Fatalf("TryAcquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	info := lk.Info()
	if info.InstanceID != "fresh-instance" {
		t.Fatalf("expected instance fresh-instance, got %q", info.InstanceID)
	}
	if info.ProxyAddr != "fresh:9000" {
		t.Fatalf("expected proxy fresh:9000, got %q", info.ProxyAddr)
	}
}

func TestIsProcessAlive_CurrentProcess(t *testing.T) {
	pid := os.Getpid()
	if !isProcessAlive(pid) {
		t.Fatalf("expected current process (pid %d) to be alive", pid)
	}
}

func TestIsProcessAlive_NonexistentPID(t *testing.T) {
	if isProcessAlive(9999999) {
		t.Fatal("expected non-existent PID to not be alive")
	}
}

func TestIsProcessAlive_ZeroPID(t *testing.T) {
	if isProcessAlive(0) {
		t.Fatal("expected PID 0 to not be alive")
	}
	if isProcessAlive(-1) {
		t.Fatal("expected negative PID to not be alive")
	}
}
