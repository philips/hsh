package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type headers []string

func (h *headers) String() string     { return strings.Join(*h, ",") }
func (h *headers) Set(v string) error { *h = append(*h, v); return nil }

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	status := flag.Int("status", 407, "status code to answer with")
	reason := flag.String("reason", "", "reason phrase (default: the standard one)")
	body := flag.String("body", "blocked\n", "response body")
	var extra headers
	flag.Var(&extra, "header", "extra response header K=V (repeatable)")
	portFile := flag.String("port-file", "", "write the chosen address here")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *portFile != "" {
		_ = os.WriteFile(*portFile, []byte(ln.Addr().String()), 0o644)
	}
	phrase := *reason
	if phrase == "" {
		phrase = http.StatusText(*status)
	}
	var count atomic.Int64
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			r := bufio.NewReader(c)
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			for {
				l, err := r.ReadString('\n')
				if err != nil || strings.TrimSpace(l) == "" {
					break
				}
			}
			n := count.Add(1)
			fmt.Printf("request %d: %s\n", n, strings.TrimSpace(line))
			var b strings.Builder
			fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", *status, phrase)
			if *status == 407 {
				b.WriteString("Proxy-Authenticate: Hsh realm=\"egress-allowlist\"\r\n")
			}
			for _, h := range extra {
				b.WriteString(h + "\r\n")
			}
			fmt.Fprintf(&b, "Content-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(*body), *body)
			_, _ = c.Write([]byte(b.String()))
		}()
	}
}
