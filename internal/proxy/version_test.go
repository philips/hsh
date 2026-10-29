package proxy

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"hsh/internal/config"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestReloadKeepsThePolicyWhenTheConfigVersionIsNewer(t *testing.T) {
	logs := &lockedBuffer{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	e := startHoldProxy(t, "allowlist", "", false)
	e.allow("a.example")
	waitFor(t, "a.example to be allowed", func() bool { return e.proxy.allowListed("a.example") })

	newer := fmt.Sprintf(`{"version": %d, "network": {"mode": "https", "allow": ["a.example", "b.example"]}}`, config.Version+1)
	if err := os.WriteFile(e.cfgPath, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed reload to be logged", func() bool {
		return strings.Contains(logs.String(), "configuration reload failed") && strings.Contains(logs.String(), "newer than this hsh reads")
	})
	if !e.proxy.allowListed("a.example") {
		t.Fatal("a failed reload dropped the allowlist")
	}
	if e.proxy.allowListed("b.example") {
		t.Fatal("a config this proxy cannot read was partly applied")
	}
	if got := e.proxy.mode(); got != "allowlist" {
		t.Fatalf("a failed reload changed the network mode to %q", got)
	}
}

func TestStartupRefusesAnUnreadableConfig(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "", false)
	for name, body := range map[string]string{
		"newer":   fmt.Sprintf(`{"version": %d}`, config.Version+1),
		"missing": `{"network": {"allow": []}}`,
		"unknown": `{"version": 1, "network": {"alow": []}}`,
	} {
		if err := os.WriteFile(e.cfgPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.proxy.reload(); err == nil {
			t.Fatalf("%s: reload accepted the config", name)
		}
	}
}
