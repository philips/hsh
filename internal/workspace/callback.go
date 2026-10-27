package workspace

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hsh/internal/netsetup"
)

func CallbackRelay(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("callback relay listen %s: %w", addr, err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go relayConn(conn)
	}
}

func relayConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 || fields[0] != "PORT" {
		fmt.Fprint(conn, "ERR protocol: want PORT <n>\n")
		return
	}
	port, err := strconv.Atoi(fields[1])
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprint(conn, "ERR invalid port\n")
		return
	}
	target, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		fmt.Fprintf(conn, "ERR nothing is listening on port %d in this session\n", port)
		return
	}
	defer target.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, target)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(target, br)
		done <- struct{}{}
	}()
	<-done
}

func OAuthDeliver(relayAddr, port, request string) error {
	conn, err := net.DialTimeout("tcp", relayAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to the session's callback relay %s: %w", relayAddr, err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "PORT %s\n%s", port, request); err != nil {
		return err
	}
	_, _ = io.Copy(os.Stdout, conn)
	return nil
}

func HTTPEcho(port string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return fmt.Errorf("http-echo listen: %w", err)
	}
	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer ln.Close()
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Print(line)
	body := "ok"
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	return nil
}

func startCallbackRelay() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "--callback-relay", netsetup.CallbackRelayAddr())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}
