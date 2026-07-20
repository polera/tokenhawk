package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAvailable(t *testing.T) {
	tests := []struct {
		installed string
		latest    string
		want      bool
	}{
		{"v0.1.4", "v0.1.5", true},
		{"0.1.4", "v1.0.0", true},
		{"v1.2.3-beta.1", "v1.2.3", true},
		{"v1.2.3", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", false},
		{"v1.2.3+build.1", "v1.2.3+build.2", false},
	}
	for _, test := range tests {
		t.Run(test.installed+"_"+test.latest, func(t *testing.T) {
			got, err := Available(test.installed, test.latest)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Available(%q, %q) = %v, want %v", test.installed, test.latest, got, test.want)
			}
		})
	}
	if _, err := Available("dev", "v1.0.0"); !errors.Is(err, ErrDevelopmentVersion) {
		t.Fatalf("development version error = %v", err)
	}
	if _, err := Available("v1.0.0", "latest"); err == nil {
		t.Fatal("expected invalid latest version error")
	}
}

func TestLatestSelectsPlatformAssets(t *testing.T) {
	apiURL := "https://api.test/latest"
	response := []byte(`{"tag_name":"v1.2.0","assets":[{"name":"tokenhawk_linux_arm64.tar.gz","browser_download_url":"https://release.test/archive"},{"name":"checksums.txt","browser_download_url":"https://release.test/checksums"}]}`)
	client := &Client{HTTPClient: routeClient(map[string][]byte{apiURL: response}), LatestURL: apiURL, GOOS: "linux", GOARCH: "arm64"}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "v1.2.0" || release.ArchiveName != "tokenhawk_linux_arm64.tar.gz" {
		t.Fatalf("unexpected release: %+v", release)
	}
}

func TestUpgradeToVerifiesAndReplacesExecutable(t *testing.T) {
	archive := tarGzip(t, "tokenhawk", []byte("new binary"))
	sum := sha256.Sum256(archive)
	archiveURL := "https://release.test/archive"
	checksumsURL := "https://release.test/checksums"
	checksums := []byte(fmt.Sprintf("%x  tokenhawk_linux_amd64.tar.gz\n", sum))

	executable := filepath.Join(t.TempDir(), "tokenhawk")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{HTTPClient: routeClient(map[string][]byte{archiveURL: archive, checksumsURL: checksums})}
	result, err := client.UpgradeTo(context.Background(), "v1.0.0", executable, Release{
		Version:      "v1.1.0",
		ArchiveName:  "tokenhawk_linux_amd64.tar.gz",
		ArchiveURL:   archiveURL,
		ChecksumsURL: checksumsURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Current != "v1.1.0" {
		t.Fatalf("unexpected result: %+v", result)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("executable = %q", got)
	}
}

func TestUpgradeToRejectsChecksumMismatch(t *testing.T) {
	archive := tarGzip(t, "tokenhawk", []byte("new binary"))
	archiveURL := "https://release.test/archive"
	checksumsURL := "https://release.test/checksums"
	checksums := []byte(fmt.Sprintf("%064x  tokenhawk_linux_amd64.tar.gz\n", 0))

	executable := filepath.Join(t.TempDir(), "tokenhawk")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{HTTPClient: routeClient(map[string][]byte{archiveURL: archive, checksumsURL: checksums})}
	_, err := client.UpgradeTo(context.Background(), "v1.0.0", executable, Release{
		Version: "v1.1.0", ArchiveName: "tokenhawk_linux_amd64.tar.gz",
		ArchiveURL: archiveURL, ChecksumsURL: checksumsURL,
	})
	if err == nil {
		t.Fatal("expected checksum error")
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old binary" {
		t.Fatalf("executable changed to %q", got)
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("tokenhawk.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("windows binary"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractBinary(archive.Bytes(), "tokenhawk_windows_amd64.zip")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "windows binary" {
		t.Fatalf("binary = %q", got)
	}
}

func tarGzip(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func routeClient(routes map[string][]byte) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, ok := routes[request.URL.String()]
		status := http.StatusOK
		if !ok {
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
}
