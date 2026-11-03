package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hsh/internal/config"
)

type echoUpstream struct {
	srv      *httptest.Server
	lastAuth atomic.Value
}

func newEchoUpstream(t *testing.T, useTLS bool) *echoUpstream {
	t.Helper()
	u := &echoUpstream{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.lastAuth.Store(r.Header.Get("Authorization"))
		if _, ok := upgradeProtocol(r.Header); !ok || r.URL.Path != "/ws" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test-accept\r\n\r\n")
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := io.WriteString(conn, "echo:"+line); err != nil {
				return
			}
		}
	})
	if useTLS {
		u.srv = httptest.NewTLSServer(h)
	} else {
		u.srv = httptest.NewServer(h)
	}
	t.Cleanup(u.srv.Close)
	return u
}

func upgradeRequest(t *testing.T, target, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", target+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	return req
}

func exchangeLines(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for _, msg := range []string{"hello\n", "second frame\n"} {
		if _, err := io.WriteString(conn, msg); err != nil {
			t.Fatal(err)
		}
		got, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if got != "echo:"+msg {
			t.Fatalf("relayed %q, want %q", got, "echo:"+msg)
		}
	}
}

func TestWebSocketUpgradeThroughIntercept(t *testing.T) {
	dir := t.TempDir()
	up := newEchoUpstream(t, true)
	host := upstreamHost(up.srv.URL)
	rootsPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.srv.Certificate().Raw})
	secrets := resolvedJSON(t, []config.ResolvedSecret{{Name: "KEY", Value: "real-key", Header: "Authorization", Prefix: "Bearer ", Hosts: []string{host}}})
	addr, caPool := startTestProxyMode(t, dir, writeConfig(t, dir, up.srv.URL), rootsPEM, "", secrets)
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	target := strings.TrimPrefix(up.srv.URL, "https://")
	fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	rbr := bufio.NewReader(raw)
	resp, err := http.ReadResponse(rbr, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	conn := tls.Client(raw, &tls.Config{RootCAs: caPool, ServerName: host})
	req := upgradeRequest(t, up.srv.URL, "/ws")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err = http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != "test-accept" || !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		t.Fatalf("handshake response: %d %v", resp.StatusCode, resp.Header)
	}
	if got := up.lastAuth.Load(); got != "Bearer real-key" {
		t.Fatalf("the handshake must carry the injected secret, upstream saw %v", got)
	}
	exchangeLines(t, conn, br)
}

func TestWebSocketUpgradePlainHTTP(t *testing.T) {
	dir := t.TempDir()
	up := newEchoUpstream(t, false)
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := upgradeRequest(t, up.srv.URL, "/ws")
	if err := req.WriteProxy(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	exchangeLines(t, conn, br)
}

func TestWebSocketUpgradeRefusedPassesThrough(t *testing.T) {
	dir := t.TempDir()
	up := newEchoUpstream(t, false)
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := upgradeRequest(t, up.srv.URL, "/not-ws")
	if err := req.WriteProxy(conn); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(string(body), "method not allowed") {
		t.Fatalf("a refused upgrade must reach the client as is: %d %q", resp.StatusCode, body)
	}
}
