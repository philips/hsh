package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/v1.2.3", http.StatusFound)
		case "/tag/v1.2.3":
			fmt.Fprintln(w, "release page")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tag, err := Available(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("got %q want v1.2.3", tag)
	}
}

func TestAvailableNonTagRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/some/other/path", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := Available(srv.URL); err == nil {
		t.Fatal("expected an error for a redirect without a /tag/ path")
	}
}

func TestAvailableUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := Available(srv.URL); err == nil {
		t.Fatal("expected an error when the latest endpoint is missing")
	}
}

func TestChecksum(t *testing.T) {
	sums := "abc123  other-asset\n" +
		"def456  hsh-linux-amd64\n" +
		"789abc  another\n"
	got, err := checksum(sums, "hsh-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "def456" {
		t.Fatalf("got %q want def456", got)
	}
	if _, err := checksum(sums, "missing-asset"); err == nil {
		t.Fatal("expected an error for a missing asset")
	}
}

func TestAssetName(t *testing.T) {
	asset, err := assetName()
	if err != nil {
		t.Fatal(err)
	}
	switch asset {
	case "hsh-linux-amd64", "hsh-linux-arm64":
	default:
		t.Fatalf("unexpected asset %q", asset)
	}
}

func TestDoChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hsh-linux-amd64"), []byte("new binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/download/v9.9.9/hsh-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dir, "hsh-linux-amd64"))
	})
	mux.HandleFunc("/download/v9.9.9/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "0000000000000000000000000000000000000000000000000000000000000000  hsh-linux-amd64\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Do(Options{BaseURL: srv.URL, Tag: "v9.9.9"})
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
}

func TestDoReplacesBinary(t *testing.T) {
	newBytes := []byte("new binary contents")
	sum := sha256.Sum256(newBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("/download/v1.0.0/hsh-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		w.Write(newBytes)
	})
	mux.HandleFunc("/download/v1.0.0/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  hsh-linux-amd64\n", hex.EncodeToString(sum[:]))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := Do(Options{BaseURL: srv.URL, Tag: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tag != "v1.0.0" {
		t.Fatalf("got tag %q want v1.0.0", res.Tag)
	}
	if res.Path == "" {
		t.Fatal("result path is empty")
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBytes) {
		t.Fatalf("replaced binary has wrong contents: %q", got)
	}
}

func TestBumpTakeReset(t *testing.T) {
	db := filepath.Join(t.TempDir(), "metrics.db")
	if n, err := Take(db); err != nil || n != 0 {
		t.Fatalf("take on missing db: got (%d, %v) want (0, nil)", n, err)
	}
	for i := 0; i < 3; i++ {
		if err := Bump(db); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Take(db)
	if err != nil || n != 3 {
		t.Fatalf("take: got (%d, %v) want (3, nil)", n, err)
	}
	n, err = Take(db)
	if err != nil || n != 0 {
		t.Fatalf("take after reset: got (%d, %v) want (0, nil)", n, err)
	}
}

func TestTakeCorruptDB(t *testing.T) {
	db := filepath.Join(t.TempDir(), "metrics.db")
	if err := os.WriteFile(db, []byte("not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Take(db)
	if err != nil {
		t.Fatalf("take on corrupt db: unexpected error %v", err)
	}
	if n != 0 {
		t.Fatalf("take on corrupt db: got %d want 0", n)
	}
}

func TestBumpParallel(t *testing.T) {
	db := filepath.Join(t.TempDir(), "metrics.db")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Bump(db); err != nil {
				t.Errorf("bump: %v", err)
			}
		}()
	}
	wg.Wait()
	n, err := Take(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("parallel bumps lost increments: got %d want 8", n)
	}
}
