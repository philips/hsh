package workspace

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"hsh/internal/netsetup"
)

func CallbackRelay(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("callback relay listen %s: %w", addr, err)
	}
	return ServeRelay(ln, allowPodSide)
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
