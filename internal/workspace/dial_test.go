package workspace

import (
	"net"
	"strconv"
	"testing"
)

func listenOn(t *testing.T, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte(host))
			conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func dialAndRead(t *testing.T, port int) string {
	t.Helper()
	conn, err := DialLocalPort(port)
	if err != nil {
		t.Fatalf("dial %d: %v", port, err)
	}
	defer conn.Close()
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	return string(buf[:n])
}

func TestDialLocalPortLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			if got := dialAndRead(t, listenOn(t, host)); got != host {
				t.Fatalf("reached %q", got)
			}
		})
	}
}

func TestDialLocalPortInterfaceAddress(t *testing.T) {
	hosts := localHosts()
	if len(hosts) <= 2 {
		t.Skip("no non-loopback interface address")
	}
	host := hosts[2]
	if got := dialAndRead(t, listenOn(t, host)); got != host {
		t.Fatalf("reached %q", got)
	}
}

func TestLocalHostsOrderAndScope(t *testing.T) {
	hosts := localHosts()
	if len(hosts) < 2 || hosts[0] != "127.0.0.1" || hosts[1] != "::1" {
		t.Fatalf("hosts = %v", hosts)
	}
	for _, h := range hosts[2:] {
		ip := net.ParseIP(h)
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			t.Fatalf("unexpected host %q in %v", h, hosts)
		}
	}
}

func TestDialLocalPortClosed(t *testing.T) {
	port, _ := strconv.Atoi(freePort(t))
	if conn, err := DialLocalPort(port); err == nil {
		conn.Close()
		t.Fatal("dial to a closed port succeeded")
	}
}
