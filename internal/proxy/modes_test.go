package proxy

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModeConfig(t *testing.T, dir, mode string) string {
	t.Helper()
	cfg := fmt.Sprintf(`{"network": {"mode": %q, "allow": []}}`, mode)
	p := filepath.Join(dir, "config.hsh")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func rawRequest(t *testing.T, addr, req string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return line
}

const connect443 = "CONNECT evil.test:443 HTTP/1.1\r\nHost: evil.test:443\r\n\r\n"
const connect8443 = "CONNECT evil.test:8443 HTTP/1.1\r\nHost: evil.test:8443\r\n\r\n"
const plaintextGet = "GET http://evil.test/ HTTP/1.1\r\nHost: evil.test\r\n\r\n"

func assertDenied(t *testing.T, addr, req, what string) {
	t.Helper()
	line := rawRequest(t, addr, req)
	if !strings.Contains(line, "407") {
		t.Fatalf("%s: expected 407, got %q", what, line)
	}
}

func assertNotDenied(t *testing.T, addr, req, what string) {
	t.Helper()
	line := rawRequest(t, addr, req)
	if strings.Contains(line, "407") {
		t.Fatalf("%s: unexpected 407: %q", what, line)
	}
}

func startModeProxy(t *testing.T, mode, override string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := writeModeConfig(t, dir, mode)
	addr, _ := startTestProxyMode(t, dir, configPath, nil, override)
	return addr
}

func TestNetworkModeHTTPS(t *testing.T) {
	addr := startModeProxy(t, "https", "")
	assertNotDenied(t, addr, connect443, "CONNECT to an arbitrary host on 443 in https mode")
	assertDenied(t, addr, connect8443, "CONNECT to a non-443 port in https mode")
	assertDenied(t, addr, plaintextGet, "plaintext to a non-allowlisted host in https mode")
}

func TestNetworkModeAllowlist(t *testing.T) {
	addr := startModeProxy(t, "allowlist", "")
	assertDenied(t, addr, connect443, "CONNECT to a non-allowlisted host in allowlist mode")
	assertDenied(t, addr, plaintextGet, "plaintext in allowlist mode")
}

func TestNetworkModeNone(t *testing.T) {
	addr := startModeProxy(t, "none", "")
	assertDenied(t, addr, connect443, "CONNECT in none mode")
}

func TestNetworkModeOverride(t *testing.T) {
	addr := startModeProxy(t, "allowlist", "https")
	assertNotDenied(t, addr, connect443, "CONNECT to 443 with the https override of an allowlist config")
}
