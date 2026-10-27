package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type broker struct {
	mu       sync.Mutex
	pending  map[string]bool
	approved map[string]bool
	shellPID int
}

func cmdBroker(args []string) error {
	fs := flag.NewFlagSet("broker", flag.ExitOnError)
	proxySock := fs.String("proxy-sock", "", "socket the proxy reports denials on")
	adminSock := fs.String("admin-sock", "", "socket the admin shell approves on")
	shellPID := fs.Int("shell-pid", 0, "pid of the session shell that owns the terminal")
	fs.Parse(args)
	if *proxySock == "" || *adminSock == "" || *shellPID == 0 {
		return fmt.Errorf("broker needs --proxy-sock, --admin-sock and --shell-pid")
	}
	b := &broker{pending: map[string]bool{}, approved: map[string]bool{}, shellPID: *shellPID}
	pl, err := listen(*proxySock)
	if err != nil {
		return err
	}
	al, err := listen(*adminSock)
	if err != nil {
		return err
	}
	go serve(pl, b.handleProxy)
	fmt.Println("broker: ready")
	serve(al, b.handleAdmin)
	return nil
}

func listen(path string) (net.Listener, error) {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return l, os.Chmod(path, 0o600)
}

func serve(l net.Listener, h func(line string) string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				return
			}
			fmt.Fprintln(c, h(strings.TrimSpace(line)))
		}()
	}
}

func (b *broker) handleProxy(line string) string {
	verb, host, _ := strings.Cut(line, " ")
	b.mu.Lock()
	defer b.mu.Unlock()
	switch verb {
	case "CHECK":
		if b.approved[host] {
			return "allow"
		}
		return "deny"
	case "DENY":
		if b.approved[host] {
			return "allow"
		}
		b.pending[host] = true
		pg, how := b.pause()
		fmt.Printf("broker: denial of %s; paused foreground pgrp %d (%s)\n", host, pg, how)
		return fmt.Sprintf("paused %d %s", pg, how)
	}
	return "error unknown verb"
}

func (b *broker) handleAdmin(line string) string {
	verb, arg, _ := strings.Cut(line, " ")
	b.mu.Lock()
	defer b.mu.Unlock()
	switch verb {
	case "LIST":
		var hosts []string
		for h := range b.pending {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		return "pending " + strings.Join(hosts, ",")
	case "APPROVE":
		if arg == "--all" {
			for h := range b.pending {
				b.approved[h] = true
			}
			b.pending = map[string]bool{}
			return "approved all"
		}
		if !b.pending[arg] {
			return "error not pending: " + arg
		}
		delete(b.pending, arg)
		b.approved[arg] = true
		return "approved " + arg
	}
	return "error unknown verb"
}

func (b *broker) pause() (int, string) {
	shellPG, fg, err := termGroups(b.shellPID)
	if err != nil || fg <= 0 || fg == shellPG {
		return 0, "no foreground job"
	}
	syscall.Kill(-fg, syscall.SIGTSTP)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if procState(fg) == 'T' {
			return fg, "SIGTSTP"
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(-fg, syscall.SIGSTOP)
	return fg, "SIGSTOP"
}

func statFields(pid int) ([]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil, fmt.Errorf("malformed stat")
	}
	return strings.Fields(s[i+2:]), nil
}

func termGroups(pid int) (pgrp, tpgid int, err error) {
	f, err := statFields(pid)
	if err != nil || len(f) < 6 {
		return 0, 0, fmt.Errorf("read stat: %v", err)
	}
	pgrp, _ = strconv.Atoi(f[2])
	tpgid, _ = strconv.Atoi(f[5])
	return pgrp, tpgid, nil
}

func procState(pid int) byte {
	f, err := statFields(pid)
	if err != nil || len(f) == 0 {
		return 0
	}
	return f[0][0]
}

func request(sock, line string) (string, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if _, err := fmt.Fprintln(c, line); err != nil {
		return "", err
	}
	resp, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(resp), err
}

func cmdProxyDeny(args []string) error {
	fs := flag.NewFlagSet("proxy-deny", flag.ExitOnError)
	sock := fs.String("proxy-sock", "", "")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("proxy-deny HOST")
	}
	resp, err := request(*sock, "DENY "+fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(resp)
	return nil
}

func cmdPending(args []string) error {
	fs := flag.NewFlagSet("pending", flag.ExitOnError)
	sock := fs.String("admin-sock", "", "")
	fs.Parse(args)
	resp, err := request(*sock, "LIST")
	if err != nil {
		return err
	}
	fmt.Println(resp)
	return nil
}

func cmdApprove(args []string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	sock := fs.String("admin-sock", "", "")
	all := fs.Bool("all", false, "")
	fs.Parse(args)
	line := "APPROVE --all"
	if !*all {
		if fs.NArg() != 1 {
			return fmt.Errorf("approve (HOST | --all)")
		}
		line = "APPROVE " + fs.Arg(0)
	}
	resp, err := request(*sock, line)
	if err != nil {
		return err
	}
	fmt.Println(resp)
	return nil
}
