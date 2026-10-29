package workspace

import (
	"bufio"
	"fmt"
	"net"
	"testing"
	"time"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
}

func startRelay(t *testing.T) string {
	t.Helper()
	return startRelayAllow(t, nil)
}

func startRelayAllow(t *testing.T, allow func(net.IP) bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		_ = ServeRelay(ln, allow)
	}()
	return ln.Addr().String()
}

func TestCallbackRelayDeliver(t *testing.T) {
	var gotRequest string
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetPort := fmt.Sprintf("%d", target.Addr().(*net.TCPAddr).Port)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return
		}
		gotRequest = line
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()

	relayAddr := startRelay(t)
	err = OAuthDeliver(relayAddr, targetPort, "GET /auth/callback?code=x&state=y HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	want := "GET /auth/callback?code=x&state=y HTTP/1.1\r\n"
	if gotRequest != want {
		t.Fatalf("target received %q, want %q", gotRequest, want)
	}
}

func TestCallbackRelayRefusesInvalidPort(t *testing.T) {
	relayAddr := startRelay(t)
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("PORT 99999\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "ERR invalid port\n" {
		t.Fatalf("relay answer = %q, want ERR invalid port", got)
	}
}

func TestCallbackRelayNothingListening(t *testing.T) {
	relayAddr := startRelay(t)
	deadPort := freePort(t)
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("PORT " + deadPort + "\n"))
	buf := make([]byte, 128)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); len(got) < 3 || got[:3] != "ERR" {
		t.Fatalf("relay answer = %q, want an ERR about nothing listening", got)
	}
}
