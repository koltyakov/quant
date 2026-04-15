package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateDownloadURL_ValidHTTPS(t *testing.T) {
	url, err := validateDownloadURL("https://github.com/owner/repo/releases/download/v1.0.0/binary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://github.com/owner/repo/releases/download/v1.0.0/binary" {
		t.Fatalf("unexpected URL: %q", url)
	}
}

func TestValidateDownloadURL_HTTP(t *testing.T) {
	_, err := validateDownloadURL("http://github.com/owner/repo/releases/download/v1.0.0/binary")
	if err == nil {
		t.Fatal("expected error for http URL")
	}
}

func TestValidateDownloadURL_NonGitHubHost(t *testing.T) {
	_, err := validateDownloadURL("https://example.com/owner/repo/releases/download/v1.0.0/binary")
	if err == nil {
		t.Fatal("expected error for non-github host")
	}
}

func TestValidateDownloadURL_MissingReleasesPath(t *testing.T) {
	_, err := validateDownloadURL("https://github.com/owner/repo/assets/binary")
	if err == nil {
		t.Fatal("expected error for missing releases/download path")
	}
}

func TestValidateDownloadURL_InvalidURL(t *testing.T) {
	_, err := validateDownloadURL("://invalid")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestValidateDownloadURL_TrailingSlash(t *testing.T) {
	url, err := validateDownloadURL("https://github.com/owner/repo/releases/download/v1.0.0/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "/releases/download/") {
		t.Fatalf("expected URL to contain releases/download path, got %q", url)
	}
}

func TestAssetNameForPlatform_UnsupportedOS(t *testing.T) {
	_, err := goosToAssetOS("freebsd")
	if err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestAssetNameForPlatform_UnsupportedArch(t *testing.T) {
	_, err := goarchToAssetArch("386")
	if err == nil {
		t.Fatal("expected error for unsupported architecture")
	}
}

func TestAssetNameForPlatform_WindowsExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		name, err := assetNameForPlatform()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(name, ".zip") {
			t.Fatalf("expected .zip extension on Windows, got %q", name)
		}
	} else {
		name, err := assetNameForPlatform()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(name, ".tar.gz") {
			t.Fatalf("expected .tar.gz extension on non-Windows, got %q", name)
		}
	}
}

func TestGetFileCaps_NonLinux(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testbin")
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	caps := getFileCaps(path)
	if runtime.GOOS != "linux" && caps != "" {
		t.Fatalf("expected empty caps on non-Linux, got %q", caps)
	}
}

func TestSetFileCaps_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testbin")
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	setFileCaps(path, "")
}

func TestSetFileCaps_NonLinux(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testbin")
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	setFileCaps(path, "cap_net_raw+ep")
}

func TestCopyFilePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")
	content := []byte("hello world copy test")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyFilePath(src, dst, 0o644); err != nil {
		t.Fatalf("copyFilePath error: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst error: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

func TestCopyFilePath_SourceNotExist(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nonexistent.txt")
	dst := filepath.Join(dir, "dest.txt")

	err := copyFilePath(src, dst, 0o644)
	if err == nil {
		t.Fatal("expected error for nonexistent source file")
	}
}

func TestCopyFilePath_DestinationAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("src content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("dst content"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := copyFilePath(src, dst, 0o644)
	if err == nil {
		t.Fatal("expected error when destination already exists (O_EXCL)")
	}
}

func TestSyncDir(t *testing.T) {
	dir := t.TempDir()
	if err := syncDir(dir); err != nil {
		t.Fatalf("syncDir error: %v", err)
	}
}

func TestSyncDir_NonExistent(t *testing.T) {
	err := syncDir("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("expected syncDir to handle nonexistent dir gracefully, got: %v", err)
	}
}

func TestIsPermissionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "permission denied", err: os.ErrPermission, want: true},
		{name: "EACCES", err: errors.New("EACCES wrapped"), want: false},
		{name: "other", err: errors.New("something else"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermissionError(tt.err); got != tt.want {
				t.Fatalf("isPermissionError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckAndApply_NoUpdateAvailable(t *testing.T) {
	tagName := "v1.0.0"
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"`+tagName+`","assets":[]}`), nil
	})

	ctx := context.Background()
	result, err := CheckAndApply(ctx, "1.0.0")
	if err != nil {
		t.Fatalf("CheckAndApply() error = %v", err)
	}
	if result.Updated {
		t.Fatal("expected Updated=false when already up to date")
	}
	if result.CurrentVersion != "1.0.0" {
		t.Fatalf("CurrentVersion = %q, want %q", result.CurrentVersion, "1.0.0")
	}
	if result.LatestVersion != "" {
		t.Fatalf("LatestVersion = %q, want empty", result.LatestVersion)
	}
}

func TestCheck_CurrentNewerThanLatest(t *testing.T) {
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"v0.1.0","assets":[]}`), nil
	})

	rel, err := Check(context.Background(), "2.0.0")
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel != nil {
		t.Fatal("expected nil release when current version is newer")
	}
}

func TestCheck_EqualVersions(t *testing.T) {
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"v1.0.0","assets":[]}`), nil
	})

	rel, err := Check(context.Background(), "1.0.0")
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel != nil {
		t.Fatal("expected nil release for equal versions")
	}
}

func TestApply_MissingAsset(t *testing.T) {
	rel := &Release{TagName: "v9.9.9", Assets: []Asset{}}
	if _, err := Apply(context.Background(), rel); err == nil {
		t.Fatal("expected error for missing release asset")
	}
}

func TestCheck_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Check(ctx, "1.0.0")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestRestart(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	_ = exe
}

func TestReleasesURL_CustomRepo(t *testing.T) {
	t.Setenv("QUANT_UPDATE_REPO", "myorg/myrepo")
	url, err := releasesURL()
	if err != nil {
		t.Fatalf("releasesURL() error = %v", err)
	}
	if !strings.Contains(url, "myorg/myrepo") {
		t.Fatalf("expected URL to contain custom repo, got %q", url)
	}
}

func TestReleasesURL_InvalidRepo(t *testing.T) {
	t.Setenv("QUANT_UPDATE_REPO", "invalid/repo/with/slashes")
	_, err := releasesURL()
	if err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

func TestValidatedGitHubRepo(t *testing.T) {
	tests := []struct {
		repo string
		ok   bool
	}{
		{"owner/repo", true},
		{"org.name/repo.name", true},
		{"owner/repo/slashes", false},
		{"singleslash", false},
	}
	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			t.Setenv("QUANT_UPDATE_REPO", tt.repo)
			got, err := validatedGitHubRepo()
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected error for invalid repo")
			}
			if tt.ok && got != tt.repo {
				t.Fatalf("got %q, want %q", got, tt.repo)
			}
		})
	}
}

func TestReplaceBinaryCopyStaged_NilOps(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
}

func TestReplaceBinaryCopyStaged_BackupRenameFails_CopySucceeds(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{
		rename: func(oldPath, newPath string) error { return errors.New("rename blocked") },
		remove: os.Remove,
		copy:   copyFilePath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
}

func TestReplaceBinaryCopyStaged_RemoveOldFails(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quant")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceBinaryCopyStaged(staged, exe, 0o755, "", replaceOps{
		rename: func(oldPath, newPath string) error { return errors.New("rename blocked") },
		remove: func(path string) error { return fmt.Errorf("remove blocked: %s", path) },
		copy:   copyFilePath,
	})
	if err == nil {
		t.Fatal("expected error when remove fails")
	}
}

func TestReplaceBinaryCopy_ExeNotExist(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "nonexistent")

	err := replaceBinaryCopy(exe, []byte("new"), "")
	if err == nil {
		t.Fatal("expected error for nonexistent executable")
	}
}

func TestSyncDir_WindowsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		dir := t.TempDir()
		if err := syncDir(dir); err != nil {
			t.Fatalf("syncDir on Windows should return nil, got %v", err)
		}
	}
}

func TestCheckAndApply_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CheckAndApply(ctx, "1.0.0")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestDownload_InvalidURL(t *testing.T) {
	_, err := download(context.Background(), "://invalid-url")
	if err == nil {
		t.Fatal("expected error for invalid download URL")
	}
}
