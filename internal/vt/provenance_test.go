package vt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func readProvenance(t *testing.T) (version, sha, sumsLine string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("libghostty", "wasmvt", "PROVENANCE"))
	if err != nil {
		t.Fatal(err)
	}

	shaRe := regexp.MustCompile(`(?m)^sha256 ([0-9a-f]{64})$`)
	sumsRe := regexp.MustCompile(`(?m)^sha256sums ([0-9a-f]{64})  (.+)$`)
	versionRe := regexp.MustCompile(`(?m)^libghostty-vt ([0-9]+\.[0-9]+\.[0-9]+)$`)

	if m := versionRe.FindStringSubmatch(string(data)); m != nil {
		version = m[1]
	} else {
		t.Fatal("PROVENANCE has no version line")
	}

	if m := shaRe.FindStringSubmatch(string(data)); m != nil {
		sha = m[1]
	} else {
		t.Fatal("PROVENANCE has no sha256 line")
	}

	if m := sumsRe.FindStringSubmatch(string(data)); m != nil {
		sumsLine = m[2]
	} else {
		t.Fatal("PROVENANCE has no sha256sums line")
	}

	return version, sha, sumsLine
}

func TestPinnedWasmBlob(t *testing.T) {
	version, sha, sumsLine := readProvenance(t)

	if sumsLine != "ghostty-vt-"+version+"-small.wasm" {
		t.Fatalf("sha256sums line names %q for version %s", sumsLine, version)
	}

	data, err := os.ReadFile(filepath.Join("libghostty", "wasmvt", "ghostty-vt-small.wasm"))
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		t.Errorf("the committed wasm hash %s differs from the pinned %s", hex.EncodeToString(sum[:]), sha)
	}
}
