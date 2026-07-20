package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/polera/tokenhawk/releases/latest"
	maxAPIResponse   = 1 << 20
	maxChecksumFile  = 1 << 20
	maxArchive       = 100 << 20
	maxBinary        = 100 << 20
)

var ErrDevelopmentVersion = errors.New("the installed version is not a released semantic version")

type Client struct {
	HTTPClient *http.Client
	LatestURL  string
	GOOS       string
	GOARCH     string
}

type Release struct {
	Version      string
	ArchiveName  string
	ArchiveURL   string
	ChecksumsURL string
}

type Result struct {
	Previous string
	Current  string
	Updated  bool
}

type releaseResponse struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		LatestURL:  latestReleaseURL,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

func (c *Client) Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.latestURL(), nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tokenhawk-updater")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("check GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("check GitHub releases: HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return Release{}, fmt.Errorf("read GitHub release: %w", err)
	}
	if len(body) > maxAPIResponse {
		return Release{}, errors.New("GitHub release response is too large")
	}
	var response releaseResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return Release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	if _, err = parseVersion(response.TagName); err != nil {
		return Release{}, fmt.Errorf("latest GitHub release has invalid version %q", response.TagName)
	}

	archiveName := "tokenhawk_" + c.goos() + "_" + c.goarch() + ".tar.gz"
	if c.goos() == "windows" {
		archiveName = "tokenhawk_" + c.goos() + "_" + c.goarch() + ".zip"
	}
	release := Release{Version: response.TagName, ArchiveName: archiveName}
	for _, asset := range response.Assets {
		switch asset.Name {
		case archiveName:
			release.ArchiveURL = asset.URL
		case "checksums.txt":
			release.ChecksumsURL = asset.URL
		}
	}
	if release.ArchiveURL == "" {
		return Release{}, fmt.Errorf("release %s has no asset for %s/%s", release.Version, c.goos(), c.goarch())
	}
	if release.ChecksumsURL == "" {
		return Release{}, fmt.Errorf("release %s has no checksums.txt", release.Version)
	}
	return release, nil
}

func Available(installed, latest string) (bool, error) {
	installedVersion, err := parseVersion(installed)
	if err != nil {
		return false, fmt.Errorf("%w: %q", ErrDevelopmentVersion, installed)
	}
	latestVersion, err := parseVersion(latest)
	if err != nil {
		return false, fmt.Errorf("invalid latest version %q", latest)
	}
	return compare(installedVersion, latestVersion) < 0, nil
}

func (c *Client) Upgrade(ctx context.Context, installed, executable string) (Result, error) {
	if _, err := Available(installed, installed); err != nil {
		return Result{}, err
	}
	release, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	return c.UpgradeTo(ctx, installed, executable, release)
}

// UpgradeTo installs a release already returned by Latest. This keeps the
// interactive offer tied to the exact release the user accepted.
func (c *Client) UpgradeTo(ctx context.Context, installed, executable string, release Release) (Result, error) {
	available, err := Available(installed, release.Version)
	if err != nil {
		return Result{}, err
	}
	result := Result{Previous: installed, Current: release.Version}
	if !available {
		return result, nil
	}
	archive, err := c.download(ctx, release.ArchiveURL, maxArchive)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", release.ArchiveName, err)
	}
	checksumFile, err := c.download(ctx, release.ChecksumsURL, maxChecksumFile)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums.txt: %w", err)
	}
	want, err := checksumFor(checksumFile, release.ArchiveName)
	if err != nil {
		return Result{}, err
	}
	got := sha256.Sum256(archive)
	if !bytes.Equal(got[:], want) {
		return Result{}, fmt.Errorf("checksum mismatch for %s", release.ArchiveName)
	}
	binary, err := extractBinary(archive, release.ArchiveName)
	if err != nil {
		return Result{}, err
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("locate current executable: %w", err)
		}
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	if err = replaceExecutable(executable, binary); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func (c *Client) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tokenhawk-updater")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("download is too large")
	}
	return body, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) latestURL() string {
	if c.LatestURL != "" {
		return c.LatestURL
	}
	return latestReleaseURL
}

func (c *Client) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Client) goarch() string {
	if c.GOARCH != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

func checksumFor(data []byte, name string) ([]byte, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[len(fields)-1], "*") != name {
			continue
		}
		checksum, err := hex.DecodeString(fields[0])
		if err != nil || len(checksum) != sha256.Size {
			return nil, fmt.Errorf("invalid checksum for %s", name)
		}
		return checksum, nil
	}
	return nil, fmt.Errorf("checksums.txt has no entry for %s", name)
}

func extractBinary(archive []byte, archiveName string) ([]byte, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open release archive: %w", err)
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) != "tokenhawk.exe" || file.FileInfo().IsDir() {
				continue
			}
			if file.UncompressedSize64 > maxBinary {
				return nil, errors.New("binary in release archive is too large")
			}
			entry, openErr := file.Open()
			if openErr != nil {
				return nil, fmt.Errorf("open binary in release archive: %w", openErr)
			}
			binary, readErr := io.ReadAll(io.LimitReader(entry, maxBinary+1))
			closeErr := entry.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read binary in release archive: %w", readErr)
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if len(binary) > maxBinary {
				return nil, errors.New("binary in release archive is too large")
			}
			return binary, nil
		}
		return nil, errors.New("release archive does not contain tokenhawk.exe")
	}

	gzipArchive, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipArchive.Close()
	reader := tar.NewReader(gzipArchive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("open release archive: %w", err)
		}
		if filepath.Base(header.Name) != "tokenhawk" || header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > maxBinary {
			return nil, errors.New("binary in release archive is too large")
		}
		binary, err := io.ReadAll(io.LimitReader(reader, maxBinary+1))
		if err != nil {
			return nil, fmt.Errorf("read binary in release archive: %w", err)
		}
		if len(binary) > maxBinary {
			return nil, errors.New("binary in release archive is too large")
		}
		return binary, nil
	}
	return nil, errors.New("release archive does not contain tokenhawk")
}

func writeReplacement(path string, binary []byte) (tempPath string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect current executable: %w", err)
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tokenhawk-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("create upgrade beside %s: %w", path, err)
	}
	tempPath = temp.Name()
	if _, err = temp.Write(binary); err == nil {
		err = temp.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("write upgraded executable: %w", err)
	}
	return tempPath, nil
}

type semanticVersion struct {
	major, minor, patch uint64
	pre                 []string
}

func parseVersion(value string) (semanticVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if build := strings.IndexByte(value, '+'); build >= 0 {
		value = value[:build]
	}
	var pre []string
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		pre = strings.Split(value[dash+1:], ".")
		value = value[:dash]
		if len(pre) == 0 {
			return semanticVersion{}, errors.New("invalid prerelease")
		}
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticVersion{}, errors.New("expected major.minor.patch")
	}
	numbers := make([]uint64, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, errors.New("invalid numeric version")
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, err
		}
		numbers[i] = n
	}
	for _, identifier := range pre {
		if identifier == "" {
			return semanticVersion{}, errors.New("invalid prerelease")
		}
		if len(identifier) > 1 && identifier[0] == '0' {
			if _, err := strconv.ParseUint(identifier, 10, 64); err == nil {
				return semanticVersion{}, errors.New("invalid numeric prerelease")
			}
		}
		for _, r := range identifier {
			if !(r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
				return semanticVersion{}, errors.New("invalid prerelease")
			}
		}
	}
	return semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2], pre: pre}, nil
}

func compare(a, b semanticVersion) int {
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) > 0 {
		return 1
	}
	if len(a.pre) > 0 && len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if a.pre[i] == b.pre[i] {
			continue
		}
		an, aerr := strconv.ParseUint(a.pre[i], 10, 64)
		bn, berr := strconv.ParseUint(b.pre[i], 10, 64)
		if aerr == nil && berr == nil {
			if an < bn {
				return -1
			}
			return 1
		}
		if aerr == nil {
			return -1
		}
		if berr == nil {
			return 1
		}
		if a.pre[i] < b.pre[i] {
			return -1
		}
		return 1
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}
