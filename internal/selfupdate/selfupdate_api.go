// Package selfupdate checks for newer releases on GitHub and replaces
// the running binary in-place.
package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	defaultGitHubRepo = "koltyakov/quant"
	projectName       = "quant"
	maxReleaseJSON    = 2 << 20   // 2 MiB
	maxDownloadBytes  = 100 << 20 // 100 MiB
	maxBinaryBytes    = 100 << 20 // 100 MiB
)

var releaseHTTPClient = &http.Client{Timeout: 20 * time.Second}
var downloadHTTPClient = &http.Client{Timeout: 5 * time.Minute}
var gitHubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Release holds the subset of GitHub release metadata we care about.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a single downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Result describes what happened during an update check.
type Result struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
	AssetName      string
}

func gitHubRepo() string {
	if repo := strings.TrimSpace(os.Getenv("QUANT_UPDATE_REPO")); repo != "" {
		return repo
	}
	return defaultGitHubRepo
}

func validatedGitHubRepo() (string, error) {
	repo := gitHubRepo()
	if !gitHubRepoPattern.MatchString(repo) {
		return "", fmt.Errorf("invalid update repo %q", repo)
	}
	return repo, nil
}

func releasesURL() (string, error) {
	repo, err := validatedGitHubRepo()
	if err != nil {
		return "", err
	}
	return "https://api.github.com/repos/" + repo + "/releases/latest", nil
}

func validateDownloadURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid download URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("download URL must use https")
	}
	if parsed.Host != "github.com" {
		return "", fmt.Errorf("download URL host must be github.com")
	}
	if !strings.Contains(parsed.Path, "/releases/download/") {
		return "", fmt.Errorf("download URL must point to a GitHub release asset")
	}
	return parsed.String(), nil
}

// Check queries GitHub for the latest release and returns nil when the current
// version is already up to date.
func Check(ctx context.Context, currentVersion string) (*Release, error) {
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return nil, err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")
	if current == latest || current == "dev" {
		return nil, nil
	}
	if !isNewer(current, latest) {
		return nil, nil
	}
	return rel, nil
}

// Apply downloads the appropriate asset from the release and replaces the
// current binary.
func Apply(ctx context.Context, rel *Release) (*Result, error) {
	assetName, err := assetNameForPlatform()
	if err != nil {
		return nil, err
	}

	var dlURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			dlURL = a.BrowserDownloadURL
			break
		}
	}
	if dlURL == "" {
		return nil, fmt.Errorf("no release asset %q found for current platform", assetName)
	}
	dlURL, err = validateDownloadURL(dlURL)
	if err != nil {
		return nil, fmt.Errorf("invalid release asset URL for %s: %w", assetName, err)
	}

	data, err := download(ctx, dlURL)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", assetName, err)
	}

	binary, err := extractBinary(assetName, data)
	if err != nil {
		return nil, fmt.Errorf("extract binary: %w", err)
	}

	if err := replaceBinary(binary); err != nil {
		return nil, fmt.Errorf("replace binary: %w", err)
	}

	return &Result{
		LatestVersion: strings.TrimPrefix(rel.TagName, "v"),
		Updated:       true,
		AssetName:     assetName,
	}, nil
}

// CheckAndApply checks for a newer release and applies it if one exists.
func CheckAndApply(ctx context.Context, currentVersion string) (*Result, error) {
	rel, err := Check(ctx, currentVersion)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return &Result{CurrentVersion: currentVersion, Updated: false}, nil
	}
	result, err := Apply(ctx, rel)
	if err != nil {
		return nil, err
	}
	result.CurrentVersion = currentVersion
	return result, nil
}

// Restart re-executes the current binary with the original arguments.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determine executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	//nolint:gosec // Restart intentionally re-execs the current binary with the current process arguments.
	return syscall.Exec(exe, os.Args, os.Environ())
}
