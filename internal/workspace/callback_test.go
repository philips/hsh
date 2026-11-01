package workspace

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
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

func TestDeliverLocal(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()
	var out strings.Builder
	port := fmt.Sprint(target.Addr().(*net.TCPAddr).Port)
	if err := deliverLocal(&out, port, "GET /auth/callback?code=x&state=y HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if line := <-got; line != "GET /auth/callback?code=x&state=y HTTP/1.1\r\n" {
		t.Fatalf("target received %q", line)
	}
	if !strings.HasPrefix(out.String(), "HTTP/1.1 200 OK") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDeliverLocalInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "99999", "x"} {
		var out strings.Builder
		if err := deliverLocal(&out, port, "GET / HTTP/1.1\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		if out.String() != "ERR invalid port\n" {
			t.Fatalf("port %q: output = %q", port, out.String())
		}
	}
}

func TestDeliverLocalNothingListening(t *testing.T) {
	var out strings.Builder
	if err := deliverLocal(&out, freePort(t), "GET / HTTP/1.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "ERR nothing is listening") {
		t.Fatalf("output = %q", out.String())
	}
}
