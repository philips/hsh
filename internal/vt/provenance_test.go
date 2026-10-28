package vt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pinnedGhosttyRevision = "7f62fe70a288c5d35ebd3097e75c46017950bcc7"
	pinnedWasmSHA256      = "98ed3473d125460daea8602cebe2cd0e5a63209c908b72f054be712c2ccdcf6c"
)

func TestPinnedWasmBlob(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "code.selman.me/hauntty").Output()
	if err != nil {
		t.Skipf("module not resolvable: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "libghostty", "internal", "wasmvt")
	data, err := os.ReadFile(filepath.Join(dir, "ghostty-vt-small.wasm.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, pinnedGhosttyRevision) {
		t.Errorf("the vendored wasm is built from a different Ghostty revision than the pinned %s:\n%s", pinnedGhosttyRevision, text)
	}
	if !strings.Contains(text, pinnedWasmSHA256) {
		t.Errorf("the vendored wasm hash changed from the pinned %s:\n%s", pinnedWasmSHA256, text)
	}
}
