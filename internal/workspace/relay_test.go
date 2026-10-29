package workspace

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listenLoopback(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, ln.Addr().(*net.TCPAddr).Port
}

func TestDialRelayRoundTripLargeWithHalfClose(t *testing.T) {
	target, port := listenLoopback(t)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		conn.Write(bytes.ToUpper(data))
	}()
	relayAddr := startRelay(t)
	conn, err := DialRelay(relayAddr, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := bytes.Repeat([]byte("hello relay "), 400000)
	go func() {
		conn.Write(payload)
		conn.(*net.TCPConn).CloseWrite()
	}()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.ToUpper(payload)) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDialRelayServerClosesFirst(t *testing.T) {
	target, port := listenLoopback(t)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("bye"))
		conn.Close()
	}()
	conn, err := DialRelay(startRelay(t), port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "bye" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDialRelayClosedPort(t *testing.T) {
	_, err := DialRelay(startRelay(t), mustPort(t, freePort(t)))
	if err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Fatalf("err = %v, want nothing is listening", err)
	}
}

func TestDialRelayInvalidPort(t *testing.T) {
	_, err := DialRelay(startRelay(t), 70000)
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("err = %v, want invalid port", err)
	}
}

func TestRelayRefusesDisallowedSource(t *testing.T) {
	target, port := listenLoopback(t)
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	relayAddr := startRelayAllow(t, func(net.IP) bool { return false })
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("DIAL " + strconv.Itoa(port) + "\n"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, _ := io.ReadAll(conn)
	if len(got) != 0 {
		t.Fatalf("a refused source got %q", got)
	}
}

func TestRelayUnknownVerb(t *testing.T) {
	conn, err := net.Dial("tcp", startRelay(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("EXEC rm\n"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, _ := io.ReadAll(conn)
	if !strings.HasPrefix(string(got), "ERR protocol") {
		t.Fatalf("got %q", got)
	}
}

func TestParseListeners(t *testing.T) {
	v4 := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 0200570A:A4D2 0100570A:1F90 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 100 0 0 10 0
`
	got, err := ParseListeners(strings.NewReader(v4), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Listener{8000, "127.0.0.1"}) || got[1] != (Listener{80, "0.0.0.0"}) {
		t.Fatalf("v4 = %+v", got)
	}
	v6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
`
	got, err = ParseListeners(strings.NewReader(v6), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Listener{8080, "::1"}) || got[1] != (Listener{3000, "::"}) {
		t.Fatalf("v6 = %+v", got)
	}
}

func TestParseListenersMalformed(t *testing.T) {
	bad := "header\n 0: ZZ:1F40 00000000:0000 0A\n"
	if _, err := ParseListeners(strings.NewReader(bad), false); err == nil {
		t.Fatal("want error for malformed address")
	}
}

func TestListRelay(t *testing.T) {
	relayAddr := startRelay(t)
	_, port := listenLoopback(t)
	got, err := ListRelay(relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range got {
		if l.Port == port {
			found = true
		}
	}
	if !found {
		t.Fatalf("port %d not in %+v", port, got)
	}
}

func mustPort(t *testing.T, s string) int {
	t.Helper()
	p, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
