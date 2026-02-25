package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-i2p/i2p-vanitygen/internal/version"
)

const (
	owner = "StormyCloudInc"
	repo  = "i2p-vanitygen"
)

// Release holds parsed information from the GitHub releases API.
type Release struct {
	TagName   string // e.g. "v1.2.0"
	HTMLURL   string // link to release page on GitHub
	AssetURL  string // direct download URL for the platform asset
	AssetSize int64  // size in bytes
}

// DownloadProgress is sent on a channel during download to report progress.
type DownloadProgress struct {
	BytesRead  int64
	TotalBytes int64 // -1 if unknown
}

// Check queries the GitHub API for the latest release and returns it
// if it is newer than the current version. Returns nil, nil if already
// up to date.
func Check(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "i2p-vanitygen/"+version.Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api: status %d", resp.StatusCode)
	}

	var gh struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("parsing github response: %w", err)
	}

	if !IsNewer(gh.TagName, version.Version) {
		return nil, nil
	}

	want := assetName()
	var assetURL string
	var assetSize int64
	for _, a := range gh.Assets {
		if strings.EqualFold(a.Name, want) {
			assetURL = a.BrowserDownloadURL
			assetSize = a.Size
			break
		}
	}
	if assetURL == "" {
		return nil, fmt.Errorf("no asset named %q in release %s", want, gh.TagName)
	}

	return &Release{
		TagName:   gh.TagName,
		HTMLURL:   gh.HTMLURL,
		AssetURL:  assetURL,
		AssetSize: assetSize,
	}, nil
}

// Download fetches the release asset to a temporary file next to the
// running binary. Progress is reported on the channel (may be nil).
// Returns the path to the downloaded file.
func Download(ctx context.Context, r *Release, progress chan<- DownloadProgress) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.AssetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "i2p-vanitygen/"+version.Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download: status %d", resp.StatusCode)
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exePath)

	tmp, err := os.CreateTemp(dir, "i2p-vanitygen-update-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 32*1024)

	for {
		select {
		case <-ctx.Done():
			tmp.Close()
			os.Remove(tmpPath)
			return "", ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := tmp.Write(buf[:n]); wErr != nil {
				tmp.Close()
				os.Remove(tmpPath)
				return "", wErr
			}
			written += int64(n)
			if progress != nil {
				select {
				case progress <- DownloadProgress{BytesRead: written, TotalBytes: total}:
				default:
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return "", readErr
		}
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	// On Unix, ensure the downloaded binary is executable.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0755); err != nil {
			os.Remove(tmpPath)
			return "", err
		}
	}

	return tmpPath, nil
}

// Apply performs self-replacement by renaming the current binary to .old
// and moving the downloaded file into its place.
func Apply(downloadedPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return err
	}

	oldPath := exePath + ".old"
	os.Remove(oldPath) // remove leftover from previous update

	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("renaming current binary: %w", err)
	}

	if err := os.Rename(downloadedPath, exePath); err != nil {
		// Try to roll back
		os.Rename(oldPath, exePath)
		return fmt.Errorf("moving new binary into place: %w", err)
	}

	return nil
}

// Restart launches a new instance of the application and exits.
func Restart() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return err
	}

	_, err = os.StartProcess(exePath, os.Args, &os.ProcAttr{
		Dir:   filepath.Dir(exePath),
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		return err
	}

	os.Exit(0)
	return nil // unreachable
}

// Cleanup removes a leftover .old file from a previous update.
// Call this early in startup. Errors are silently ignored.
func Cleanup() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	os.Remove(exePath + ".old")
}

// IsNewer returns true if remote is a newer version than local.
// Versions are expected as "vMAJOR.MINOR.PATCH".
// Dev builds never trigger update prompts.
func IsNewer(remote, local string) bool {
	if local == "dev" {
		return false
	}
	rParts := parseVersion(remote)
	lParts := parseVersion(local)
	if rParts == nil || lParts == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if rParts[i] > lParts[i] {
			return true
		}
		if rParts[i] < lParts[i] {
			return false
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	result := make([]int, 3)
	for i, p := range parts {
		if idx := strings.IndexByte(p, '-'); idx >= 0 {
			p = p[:idx]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}

// assetName returns the expected release asset filename for the current platform.
func assetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("i2p-vanitygen-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
}
