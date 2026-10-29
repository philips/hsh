package workspace

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"hsh/internal/netsetup"
)

const relayDialTimeout = 3 * time.Second

func allowPodSide(ip net.IP) bool {
	return ip.Equal(net.ParseIP(netsetup.HostIP))
}

func ServeRelay(ln net.Listener, allow func(net.IP) bool) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		if !sourceAllowed(conn, allow) {
			conn.Close()
			continue
		}
		go relayConn(conn)
	}
}

func sourceAllowed(conn net.Conn, allow func(net.IP) bool) bool {
	if allow == nil {
		return true
	}
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	return allow(tcp.IP)
}

func parsePort(s string) (int, bool) {
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func relayConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})
	fields := strings.Fields(line)
	if len(fields) == 0 {
		fmt.Fprint(conn, "ERR protocol: want PORT <n>, DIAL <n>, or LIST\n")
		return
	}
	switch fields[0] {
	case "LIST":
		listeners, err := InnerListeners()
		if err != nil {
			fmt.Fprintf(conn, "ERR %v\n", err)
			return
		}
		for _, l := range listeners {
			if strconv.Itoa(l.Port) == netsetup.CallbackRelayPort {
				continue
			}
			fmt.Fprintf(conn, "%d %s\n", l.Port, l.Addr)
		}
	case "PORT", "DIAL":
		if len(fields) != 2 {
			fmt.Fprintf(conn, "ERR protocol: want %s <n>\n", fields[0])
			return
		}
		port, ok := parsePort(fields[1])
		if !ok {
			fmt.Fprint(conn, "ERR invalid port\n")
			return
		}
		target, err := dialSandboxPort(port)
		if err != nil {
			fmt.Fprintf(conn, "ERR nothing is listening on port %d in this session\n", port)
			return
		}
		defer target.Close()
		if fields[0] == "DIAL" {
			fmt.Fprint(conn, "OK\n")
		}
		Splice(conn, br, target)
	default:
		fmt.Fprint(conn, "ERR protocol: want PORT <n>, DIAL <n>, or LIST\n")
	}
}

func dialSandboxPort(port int) (net.Conn, error) {
	p := strconv.Itoa(port)
	var lastErr error
	for _, host := range []string{"127.0.0.1", netsetup.InnerIP, "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, p), relayDialTimeout)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type closeWriter interface {
	CloseWrite() error
}

func Splice(a net.Conn, aReader io.Reader, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		if cw, ok := a.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, aReader)
		if cw, ok := b.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

func DialRelay(relayAddr string, port int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", relayAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to the session's relay %s: %w", relayAddr, err)
	}
	if _, err := fmt.Fprintf(conn, "DIAL %d\n", port); err != nil {
		conn.Close()
		return nil, err
	}
	var ack []byte
	one := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(relayDialTimeout * 4))
	for len(ack) < 512 {
		if _, err := conn.Read(one); err != nil {
			conn.Close()
			return nil, fmt.Errorf("relay closed before answering: %w", err)
		}
		if one[0] == '\n' {
			break
		}
		ack = append(ack, one[0])
	}
	conn.SetReadDeadline(time.Time{})
	if got := strings.TrimSpace(string(ack)); got != "OK" {
		conn.Close()
		return nil, fmt.Errorf("%s", strings.TrimPrefix(got, "ERR "))
	}
	return conn, nil
}

func ListRelay(relayAddr string) ([]Listener, error) {
	conn, err := net.DialTimeout("tcp", relayAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to the session's relay %s: %w", relayAddr, err)
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "LIST\n"); err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var out []Listener
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		text := sc.Text()
		if strings.HasPrefix(text, "ERR") {
			return nil, fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(text, "ERR")))
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			continue
		}
		port, ok := parsePort(fields[0])
		if !ok {
			continue
		}
		out = append(out, Listener{Port: port, Addr: fields[1]})
	}
	return out, sc.Err()
}
