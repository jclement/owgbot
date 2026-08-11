// Package selfupdate replaces the running binary with the latest GitHub
// release build. Flow: check latest release tag → download the matching
// asset → verify sha256 against checksums.txt → atomically swap the binary
// (keeping the old one as .old for manual rollback). The caller then exits;
// systemd (Restart=always) brings up the new build.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 5 * time.Minute}

// Release describes the latest published release.
type Release struct {
	Tag    string
	assets map[string]string // name → download URL
}

// Check fetches the latest release for repo ("owner/name").
func Check(repo string) (*Release, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: http %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	r := &Release{Tag: rel.TagName, assets: make(map[string]string)}
	for _, a := range rel.Assets {
		r.assets[a.Name] = a.URL
	}
	return r, nil
}

// AssetName is the release asset for this OS/arch (e.g. owgbot-linux-arm64).
func AssetName() string {
	return fmt.Sprintf("owgbot-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// Apply downloads the release asset for this platform, verifies its checksum,
// and swaps it in over the current executable.
func (r *Release) Apply() error {
	asset := AssetName()
	url, ok := r.assets[asset]
	if !ok {
		return fmt.Errorf("release %s has no asset %s", r.Tag, asset)
	}
	sumsURL, ok := r.assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", r.Tag)
	}

	wantSum, err := fetchChecksum(sumsURL, asset)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}

	newPath := exe + ".new"
	if err := download(url, newPath, wantSum); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}

	oldPath := exe + ".old"
	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("stash current binary: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		// Try to roll back so we aren't left without a binary.
		os.Rename(oldPath, exe)
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

func fetchChecksum(url, asset string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

func download(url, dest, wantSum string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: http %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return fmt.Errorf("checksum mismatch (got %s want %s)", got[:12], wantSum[:12])
	}
	return nil
}
