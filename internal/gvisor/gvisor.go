package gvisor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrNotEmbedded = errors.New("this hsh build has no embedded gVisor")

const completeMarker = ".complete"

func Embedded() bool { return len(payload) > 0 }

func Version() string { return strings.TrimSpace(payloadVersion) }

func Install(root string) (string, error) {
	if !Embedded() {
		return "", ErrNotEmbedded
	}
	return install(root, payload, strings.TrimSpace(payloadSHA256))
}

func Dir(root string) string { return filepath.Join(root, "gvisor") }

func install(root string, archive []byte, want string) (string, error) {
	if len(want) < 16 {
		sum := sha256.Sum256(archive)
		want = hex.EncodeToString(sum[:])
	}
	base := Dir(root)
	if err := ensurePrivateDir(base); err != nil {
		return "", err
	}
	dir := filepath.Join(base, want[:16])
	runsc := filepath.Join(dir, "runsc")
	if complete(dir) {
		return runsc, nil
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return "", fmt.Errorf("the embedded gVisor does not match its recorded checksum")
	}
	tmp, err := os.MkdirTemp(base, ".extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := extract(archive, tmp); err != nil {
		return "", fmt.Errorf("extract the embedded gVisor: %w", err)
	}
	if info, err := os.Stat(filepath.Join(tmp, "runsc")); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("the embedded gVisor has no runsc")
	}
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), []byte(want+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if complete(dir) {
			return runsc, nil
		}
		return "", fmt.Errorf("install the embedded gVisor into %s: %w (remove that directory and retry)", dir, err)
	}
	return runsc, nil
}

func complete(dir string) bool {
	if ownedPrivateDir(dir) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, completeMarker))
	return err == nil && info.Mode().IsRegular()
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return ownedPrivateDir(dir)
}

func ownedPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users", dir)
	}
	return nil
}

func extract(archive []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("refusing archive entry %q outside the destination", h.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0o700)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(f, tr, h.Size); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("refusing archive entry %q of type %c", h.Name, h.Typeflag)
		}
	}
}

func Prune(root, current string, inUse func(dir string) bool) {
	entries, err := os.ReadDir(Dir(root))
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(Dir(root), e.Name())
		if strings.HasPrefix(e.Name(), ".extract-") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if dir == current || inUse(dir) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}
