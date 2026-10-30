package gvisor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type entry struct {
	name string
	typ  byte
	mode int64
	body string
	link string
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func good(t *testing.T) []byte {
	return archive(t,
		entry{name: "runsc", typ: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\n"},
		entry{name: "gvisor-bin/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "gvisor-bin/gvisor_sentry", typ: tar.TypeReg, mode: 0o755, body: "sentry"},
		entry{name: "LICENSE", typ: tar.TypeReg, mode: 0o644, body: "Apache"},
	)
}

func TestInstallExtractsPrivatelyAndReuses(t *testing.T) {
	root := t.TempDir()
	runsc, err := install(root, good(t), "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(runsc)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runsc mode = %v, %v; want 0700", info.Mode(), err)
	}
	if lic, _ := os.Stat(filepath.Join(filepath.Dir(runsc), "LICENSE")); lic.Mode().Perm() != 0o600 {
		t.Fatalf("LICENSE mode = %v, want 0600", lic.Mode())
	}
	dir, _ := os.Stat(filepath.Dir(runsc))
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("install dir mode = %v, want 0700", dir.Mode())
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(runsc), "gvisor-bin", "gvisor_sentry"), []byte("kept"), 0o700); err != nil {
		t.Fatal(err)
	}
	again, err := install(root, good(t), "")
	if err != nil || again != runsc {
		t.Fatalf("second install = %q, %v; want %q", again, err, runsc)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(runsc), "gvisor-bin", "gvisor_sentry")); string(b) != "kept" {
		t.Fatal("a completed install was extracted again")
	}
}

func TestInstallKeysByContent(t *testing.T) {
	root := t.TempDir()
	a, err := install(root, good(t), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := install(root, archive(t, entry{name: "runsc", typ: tar.TypeReg, mode: 0o755, body: "other"}), "")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different payloads share an install directory")
	}
}

func TestInstallRejectsUnsafeArchives(t *testing.T) {
	cases := map[string][]byte{
		"traversal": archive(t, entry{name: "../evil", typ: tar.TypeReg, mode: 0o755, body: "x"}),
		"absolute":  archive(t, entry{name: "/evil", typ: tar.TypeReg, mode: 0o755, body: "x"}),
		"symlink":   archive(t, entry{name: "runsc", typ: tar.TypeSymlink, link: "/bin/sh"}),
		"hardlink":  archive(t, entry{name: "runsc", typ: tar.TypeLink, link: "/bin/sh"}),
		"no runsc":  archive(t, entry{name: "LICENSE", typ: tar.TypeReg, mode: 0o644, body: "x"}),
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := install(root, a, ""); err == nil {
				t.Fatal("install accepted an unsafe archive")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil")); err == nil {
				t.Fatal("an entry escaped the destination")
			}
			entries, _ := os.ReadDir(Dir(root))
			if len(entries) != 0 {
				t.Fatalf("a failed install left %d entries behind", len(entries))
			}
		})
	}
}

func TestInstallRefusesForeignOrOpenDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(root), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := install(root, good(t), ""); err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Fatalf("install into a world-writable dir: %v", err)
	}
	root2 := t.TempDir()
	if err := os.Symlink(t.TempDir(), Dir(root2)); err != nil {
		t.Fatal(err)
	}
	if _, err := install(root2, good(t), ""); err == nil {
		t.Fatal("install followed a symlinked gvisor dir")
	}
}

func TestInstallIgnoresIncompleteDir(t *testing.T) {
	root := t.TempDir()
	runsc, err := install(root, good(t), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(runsc)
	if err := os.Remove(filepath.Join(dir, completeMarker)); err != nil {
		t.Fatal(err)
	}
	if _, err := install(root, good(t), ""); err == nil {
		t.Fatal("install reused or replaced an incomplete directory without saying so")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := install(root, good(t), ""); err != nil {
		t.Fatalf("install after removing the incomplete directory: %v", err)
	}
}

func TestInstallConcurrent(t *testing.T) {
	root := t.TempDir()
	a := good(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	paths := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := install(root, a, "")
			errs <- err
			paths <- p
		}()
	}
	wg.Wait()
	close(errs)
	close(paths)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for p := range paths {
		if first == "" {
			first = p
		}
		if p != first {
			t.Fatalf("concurrent installs disagree: %q vs %q", p, first)
		}
	}
	entries, _ := os.ReadDir(Dir(root))
	if len(entries) != 1 {
		t.Fatalf("concurrent installs left %d entries, want 1", len(entries))
	}
}

func TestInstallChecksPayloadSum(t *testing.T) {
	root := t.TempDir()
	if _, err := install(root, good(t), strings.Repeat("0", 64)); err == nil {
		t.Fatal("install accepted a payload that does not match its recorded checksum")
	}
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	cur, err := install(root, good(t), "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := install(root, archive(t, entry{name: "runsc", typ: tar.TypeReg, mode: 0o755, body: "old"}), "")
	if err != nil {
		t.Fatal(err)
	}
	used, err := install(root, archive(t, entry{name: "runsc", typ: tar.TypeReg, mode: 0o755, body: "used"}), "")
	if err != nil {
		t.Fatal(err)
	}
	Prune(root, filepath.Dir(cur), func(dir string) bool { return dir == filepath.Dir(used) })
	for p, want := range map[string]bool{cur: true, used: true, old: false} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", p, err == nil, want)
		}
	}
}
