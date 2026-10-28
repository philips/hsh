package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultReleaseBase = "https://github.com/philips/hsh/releases"
	assetPrefix        = "hsh-"
	downloadTimeout    = 30 * time.Second
)

type Options struct {
	BaseURL string
	Tag     string
}

type Result struct {
	Tag  string
	Path string
}

func Available(baseURL string) (string, error) {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Head(baseURL + "/latest")
	if err != nil {
		return "", fmt.Errorf("resolve the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve the latest release: unexpected status %s", resp.Status)
	}
	if resp.Request == nil || resp.Request.URL == nil {
		return "", fmt.Errorf("could not determine the latest version")
	}
	path := resp.Request.URL.Path
	i := strings.LastIndex(path, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("could not determine the latest version")
	}
	tag := path[i+len("/tag/"):]
	if !strings.HasPrefix(tag, "v") || strings.Contains(tag, "/") {
		return "", fmt.Errorf("could not determine the latest version")
	}
	return tag, nil
}

func assetName() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return assetPrefix + "linux-amd64", nil
	case "arm64":
		return assetPrefix + "linux-arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %s (hsh releases cover linux/amd64 and linux/arm64)", runtime.GOARCH)
	}
}

func download(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	return nil
}

func checksum(sums, asset string) (string, error) {
	for line := range strings.Lines(sums) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum found for %s", asset)
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func replaceSelf(tmpPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("locate the running binary: %w", err)
	}
	dir := filepath.Dir(exe)
	stage, err := os.CreateTemp(dir, ".hsh-update-")
	if err != nil {
		return "", fmt.Errorf("could not write to %s (hsh update only manages an existing install — re-run install.sh, or set HSH_INSTALL_DIR for a writable location)", dir)
	}
	stageName := stage.Name()
	defer os.Remove(stageName)
	if err := stage.Close(); err != nil {
		return "", err
	}
	if err := copyFile(tmpPath, stageName); err != nil {
		return "", err
	}
	if err := os.Chmod(stageName, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(stageName, exe); err != nil {
		return "", fmt.Errorf("could not write to %s (hsh update only manages an existing install — re-run install.sh, or set HSH_INSTALL_DIR for a writable location)", dir)
	}
	return exe, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func Do(opts Options) (Result, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultReleaseBase
	}
	client := &http.Client{Timeout: downloadTimeout}

	asset, err := assetName()
	if err != nil {
		return Result{}, err
	}

	tmp, err := os.MkdirTemp("", "hsh-update-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)

	assetPath := filepath.Join(tmp, asset)
	sumsPath := filepath.Join(tmp, "SHA256SUMS")
	base = strings.TrimSuffix(base, "/")
	if err := download(client, base+"/download/"+opts.Tag+"/"+asset, assetPath); err != nil {
		return Result{}, err
	}
	if err := download(client, base+"/download/"+opts.Tag+"/SHA256SUMS", sumsPath); err != nil {
		return Result{}, err
	}

	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return Result{}, err
	}
	expected, err := checksum(string(sums), asset)
	if err != nil {
		return Result{}, err
	}
	actual, err := fileChecksum(assetPath)
	if err != nil {
		return Result{}, err
	}
	if expected != actual {
		return Result{}, fmt.Errorf("checksum mismatch for %s", asset)
	}

	path, err := replaceSelf(assetPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Tag: opts.Tag, Path: path}, nil
}
