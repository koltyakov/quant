package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIsNewerExported(t *testing.T) {
	if !IsNewer("1.2.2", "1.2.3") {
		t.Fatal("IsNewer() = false, want true")
	}
	if IsNewer("1.2.3", "1.2.3") {
		t.Fatal("IsNewer() = true for identical versions, want false")
	}
}

func TestExtractBinary(t *testing.T) {
	tarData := makeTarGzArchive(t, "bin/quant", []byte("tar-binary"))
	got, err := extractBinary("quant_Linux_x86_64.tar.gz", tarData)
	if err != nil {
		t.Fatalf("extractBinary(tar) error = %v", err)
	}
	if string(got) != "tar-binary" {
		t.Fatalf("extractBinary(tar) = %q, want %q", string(got), "tar-binary")
	}

	zipData := makeZipArchive(t, "release/quant.exe", []byte("zip-binary"))
	got, err = extractBinary("quant_Windows_x86_64.zip", zipData)
	if err != nil {
		t.Fatalf("extractBinary(zip) error = %v", err)
	}
	if string(got) != "zip-binary" {
		t.Fatalf("extractBinary(zip) = %q, want %q", string(got), "zip-binary")
	}
}

func TestFetchLatestRelease(t *testing.T) {
	responseStatus := http.StatusOK
	responseBody := `{"tag_name":"v1.2.3","assets":[]}`
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/repos/"+gitHubRepo()+"/releases/latest" {
			return jsonResponse(http.StatusNotFound, `not found`), nil
		}
		return jsonResponse(responseStatus, responseBody), nil
	})

	rel, err := fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if rel.TagName != "v1.2.3" {
		t.Fatalf("TagName = %q, want %q", rel.TagName, "v1.2.3")
	}

	responseBody = `{"assets":[]}`
	if _, err := fetchLatestRelease(context.Background()); err == nil || !strings.Contains(err.Error(), "missing tag_name") {
		t.Fatalf("fetchLatestRelease() error = %v, want missing tag_name error", err)
	}

	responseStatus = http.StatusBadGateway
	responseBody = `bad gateway`
	if _, err := fetchLatestRelease(context.Background()); err == nil || !strings.Contains(err.Error(), "GitHub API returned 502 Bad Gateway") {
		t.Fatalf("fetchLatestRelease() error = %v, want status error", err)
	}
}

func TestDownload(t *testing.T) {
	useDownloadTransport(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/ok":
			return responseWithLength(http.StatusOK, "binary", 6), nil
		case "/large":
			return responseWithLength(http.StatusOK, "", 104857601), nil
		default:
			return jsonResponse(http.StatusBadGateway, "nope"), nil
		}
	})

	data, err := download(context.Background(), "https://downloads.example/ok")
	if err != nil {
		t.Fatalf("download(ok) error = %v", err)
	}
	if string(data) != "binary" {
		t.Fatalf("download(ok) = %q, want %q", string(data), "binary")
	}

	if _, err := download(context.Background(), "https://downloads.example/status"); err == nil || !strings.Contains(err.Error(), "download returned 502 Bad Gateway") {
		t.Fatalf("download(status) error = %v, want status error", err)
	}
	if _, err := download(context.Background(), "https://downloads.example/large"); err == nil || !strings.Contains(err.Error(), "download too large") {
		t.Fatalf("download(large) error = %v, want size error", err)
	}
}

func TestCheck(t *testing.T) {
	tagName := "v1.2.3"
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"`+tagName+`","assets":[]}`), nil
	})

	rel, err := Check(context.Background(), "v1.2.2")
	if err != nil {
		t.Fatalf("Check(newer) error = %v", err)
	}
	if rel == nil || rel.TagName != tagName {
		t.Fatalf("Check(newer) = %#v, want release %q", rel, tagName)
	}

	rel, err = Check(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatalf("Check(same) error = %v", err)
	}
	if rel != nil {
		t.Fatalf("Check(same) = %#v, want nil", rel)
	}

	rel, err = Check(context.Background(), "dev")
	if err != nil {
		t.Fatalf("Check(dev) error = %v", err)
	}
	if rel != nil {
		t.Fatalf("Check(dev) = %#v, want nil", rel)
	}
}

func TestApplyAndCheckAndApplyErrorPaths(t *testing.T) {
	assetName, err := assetNameForPlatform()
	if err != nil {
		t.Fatalf("assetNameForPlatform() error = %v", err)
	}

	downloadURL := "https://downloads.example/asset"
	useDownloadTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, "not an archive"), nil
	})

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: downloadURL},
		},
	}
	if _, err := Apply(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "extract binary") {
		t.Fatalf("Apply() error = %v, want extract error", err)
	}

	if _, err := Apply(context.Background(), &Release{TagName: "v9.9.9"}); err == nil || !strings.Contains(err.Error(), "no release asset") {
		t.Fatalf("Apply(missing asset) error = %v, want missing asset error", err)
	}

	releaseTag := "v1.2.3"
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"`+releaseTag+`","assets":[]}`), nil
	})

	result, err := CheckAndApply(context.Background(), "1.2.3")
	if err != nil {
		t.Fatalf("CheckAndApply(no update) error = %v", err)
	}
	if result.Updated {
		t.Fatalf("CheckAndApply(no update) Updated = true, want false")
	}
	if result.CurrentVersion != "1.2.3" {
		t.Fatalf("CheckAndApply(no update) CurrentVersion = %q, want %q", result.CurrentVersion, "1.2.3")
	}

	releaseTag = "v9.9.9"
	useReleaseTransport(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"v9.9.9","assets":[{"name":"`+assetName+`","browser_download_url":"`+downloadURL+`"}]}`), nil
	})
	result, err = CheckAndApply(context.Background(), "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "extract binary") {
		t.Fatalf("CheckAndApply(update error) error = %v, want extract error", err)
	}
	if result != nil {
		t.Fatalf("CheckAndApply(update error) result = %#v, want nil", result)
	}
}

func useReleaseTransport(t *testing.T, fn roundTripFunc) {
	t.Helper()

	previous := releaseHTTPClient
	t.Cleanup(func() { releaseHTTPClient = previous })
	releaseHTTPClient = &http.Client{
		Transport: fn,
	}
}

func useDownloadTransport(t *testing.T, fn roundTripFunc) {
	t.Helper()

	previous := downloadHTTPClient
	t.Cleanup(func() { downloadHTTPClient = previous })
	downloadHTTPClient = &http.Client{Transport: fn}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonResponse(status int, body string) *http.Response {
	return responseWithLength(status, body, int64(len(body)))
}

func responseWithLength(status int, body string, contentLength int64) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		ContentLength: contentLength,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
	}
}

func makeTarGzArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close(tar) error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("Close(gzip) error = %v", err)
	}
	return buf.Bytes()
}

func makeZipArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return buf.Bytes()
}
