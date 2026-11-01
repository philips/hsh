package workspace

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
)

func DeliverLocal(port, request string) error {
	return deliverLocal(os.Stdout, port, request)
}

func deliverLocal(w io.Writer, port, request string) error {
	n, ok := parsePort(port)
	if !ok {
		fmt.Fprintln(w, "ERR invalid port")
		return nil
	}
	conn, err := DialLocalPort(n)
	if err != nil {
		fmt.Fprintf(w, "ERR nothing is listening on port %d in this session\n", n)
		return nil
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, request); err != nil {
		return err
	}
	_, _ = io.Copy(w, conn)
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
