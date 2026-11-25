package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type transport struct {
	name string
	typ  int
}

var transports = []transport{
	{"SOCK_SEQPACKET", syscall.SOCK_SEQPACKET},
	{"SOCK_DGRAM", syscall.SOCK_DGRAM},
	{"SOCK_STREAM", syscall.SOCK_STREAM},
}

const (
	bigBuf       = 4 << 20
	maxProbe     = 4 << 20
	throughputMB = 256
)

var packetSizes = []int{1, 1500, 9000, 65520}

func main() {
	fmt.Printf("host: %s/%s %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	if out, err := exec.Command("uname", "-a").Output(); err == nil {
		fmt.Printf("uname: %s", out)
	}
	printSysctls()
	printPathLimits()
	for _, t := range transports {
		fmt.Printf("\n== %s\n", t.name)
		probe(t)
	}
	fmt.Printf("\n== Go net package\n")
	probeNet("unixpacket")
	probeNet("unixgram")
	probeNet("unix")
}

func printSysctls() {
	var names []string
	switch runtime.GOOS {
	case "darwin":
		names = []string{
			"kern.ipc.maxsockbuf",
			"net.local.dgram.maxdgram",
			"net.local.dgram.recvspace",
			"net.local.stream.sendspace",
			"net.local.stream.recvspace",
		}
	case "linux":
		names = []string{
			"net.core.rmem_default",
			"net.core.rmem_max",
			"net.core.wmem_default",
			"net.core.wmem_max",
			"net.unix.max_dgram_qlen",
		}
	}
	for _, n := range names {
		out, err := exec.Command("sysctl", "-n", n).Output()
		if err != nil {
			fmt.Printf("sysctl %s: %v\n", n, err)
			continue
		}
		fmt.Printf("sysctl %s = %s\n", n, strings.TrimSpace(string(out)))
	}
}

func printPathLimits() {
	limit := len(syscall.RawSockaddrUnix{}.Path)
	fmt.Printf("sun_path: %d bytes (a path must be shorter)\n", limit)
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local/state/hsh/sessions/0123456789ab/netstack/sock"),
		filepath.Join(home, "Library/Application Support/hsh/sessions/0123456789ab/netstack/sock"),
		filepath.Join(os.TempDir(), "hsh-501/0123456789ab/netstack/sock"),
	}
	for _, c := range candidates {
		verdict := "fits"
		if len(c) >= limit {
			verdict = "TOO LONG"
		}
		fmt.Printf("  %3d %-8s %s\n", len(c), verdict, c)
	}
}

func probe(t transport) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, t.typ, 0)
	if err != nil {
		fmt.Printf("socketpair: FAIL %v\n", err)
	} else {
		fmt.Printf("socketpair: ok\n")
		snd, _ := syscall.GetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF)
		rcv, _ := syscall.GetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		fmt.Printf("default buffers: SO_SNDBUF=%d SO_RCVBUF=%d\n", snd, rcv)
		closePair(fds)
		setBufResult(t.typ)
	}

	fmt.Printf("listen/connect on a path: %s\n", pathTest(t.typ))

	if err != nil {
		return
	}

	if t.typ != syscall.SOCK_STREAM {
		fmt.Printf("record boundaries (default buffers): %s\n", boundaries(t.typ, 0))
		fmt.Printf("record boundaries (4 MiB buffers):   %s\n", boundaries(t.typ, bigBuf))
		fmt.Printf("largest message (default buffers): %s\n", maxMessage(t.typ, 0))
		fmt.Printf("largest message (4 MiB buffers):   %s\n", maxMessage(t.typ, bigBuf))
		fmt.Printf("short read buffer: %s\n", truncation(t.typ))
	}
	fmt.Printf("peer closed, read: %s\n", eofRead(t.typ))
	fmt.Printf("peer closed, write: %s\n", eofWrite(t.typ))
	for _, size := range []int{1500, 65520} {
		fmt.Printf("throughput %5d-byte packets, 4 MiB buffers: %s\n", size, throughput(t.typ, size, bigBuf))
	}
}

func closePair(fds [2]int) {
	syscall.Close(fds[0])
	syscall.Close(fds[1])
}

func newPair(typ, buf int) ([2]int, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, typ, 0)
	if err != nil {
		return fds, err
	}
	if buf > 0 {
		for _, fd := range fds {
			_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, buf)
			_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, buf)
		}
	}
	return fds, nil
}

func setBufResult(typ int) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, typ, 0)
	if err != nil {
		return
	}
	defer closePair(fds)
	e1 := syscall.SetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF, bigBuf)
	e2 := syscall.SetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_RCVBUF, bigBuf)
	snd, _ := syscall.GetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	rcv, _ := syscall.GetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	fmt.Printf("set 4 MiB buffers: SO_SNDBUF err=%v now %d; SO_RCVBUF err=%v now %d\n", e1, snd, e2, rcv)
}

func boundaries(typ, buf int) string {
	fds, err := newPair(typ, buf)
	if err != nil {
		return "FAIL " + err.Error()
	}
	defer closePair(fds)
	_ = syscall.SetNonblock(fds[0], true)
	var parts []string
	rbuf := make([]byte, maxProbe+1)
	for _, size := range packetSizes {
		msg := pattern(size)
		n, err := syscall.Write(fds[0], msg)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%d:send %v", size, err))
			continue
		}
		if n != size {
			parts = append(parts, fmt.Sprintf("%d:sent %d", size, n))
			continue
		}
		rn, _, flags, _, err := syscall.Recvmsg(fds[1], rbuf, nil, 0)
		switch {
		case err != nil:
			parts = append(parts, fmt.Sprintf("%d:recv %v", size, err))
		case rn != size:
			parts = append(parts, fmt.Sprintf("%d:got %d", size, rn))
		case flags&syscall.MSG_TRUNC != 0:
			parts = append(parts, fmt.Sprintf("%d:MSG_TRUNC", size))
		case string(rbuf[:rn]) != string(msg):
			parts = append(parts, fmt.Sprintf("%d:corrupt", size))
		default:
			parts = append(parts, fmt.Sprintf("%d:ok", size))
		}
	}
	return strings.Join(parts, " ")
}

func sendOne(typ, buf, size int) error {
	fds, err := newPair(typ, buf)
	if err != nil {
		return err
	}
	defer closePair(fds)
	_ = syscall.SetNonblock(fds[0], true)
	n, err := syscall.Write(fds[0], make([]byte, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("short write %d", n)
	}
	rbuf := make([]byte, size+1)
	rn, err := syscall.Read(fds[1], rbuf)
	if err != nil {
		return err
	}
	if rn != size {
		return fmt.Errorf("read %d", rn)
	}
	return nil
}

func maxMessage(typ, buf int) string {
	if sendOne(typ, buf, 1) != nil {
		return "cannot send 1 byte"
	}
	if sendOne(typ, buf, maxProbe) == nil {
		return fmt.Sprintf(">= %d bytes", maxProbe)
	}
	lo, hi := 1, maxProbe
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if sendOne(typ, buf, mid) == nil {
			lo = mid
		} else {
			hi = mid
		}
	}
	return fmt.Sprintf("%d bytes (%d fails: %v)", lo, hi, sendOne(typ, buf, hi))
}

func truncation(typ int) string {
	fds, err := newPair(typ, bigBuf)
	if err != nil {
		return "FAIL " + err.Error()
	}
	defer closePair(fds)
	if _, err := syscall.Write(fds[0], pattern(2000)); err != nil {
		return "send " + err.Error()
	}
	if _, err := syscall.Write(fds[0], pattern(10)); err != nil {
		return "send " + err.Error()
	}
	small := make([]byte, 1000)
	n, _, flags, _, err := syscall.Recvmsg(fds[1], small, nil, 0)
	if err != nil {
		return "recv " + err.Error()
	}
	next := make([]byte, 4000)
	n2, err := syscall.Read(fds[1], next)
	if err != nil {
		return fmt.Sprintf("first read %d trunc=%v, second read %v", n, flags&syscall.MSG_TRUNC != 0, err)
	}
	return fmt.Sprintf("2000-byte record into 1000-byte buffer: read %d, MSG_TRUNC=%v; next read %d (10 means the rest was discarded)", n, flags&syscall.MSG_TRUNC != 0, n2)
}

func eofRead(typ int) string {
	fds, err := newPair(typ, 0)
	if err != nil {
		return "FAIL " + err.Error()
	}
	defer syscall.Close(fds[1])
	tv := syscall.NsecToTimeval(int64(500 * time.Millisecond))
	_ = syscall.SetsockoptTimeval(fds[1], syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	syscall.Close(fds[0])
	n, err := syscall.Read(fds[1], make([]byte, 100))
	if err != nil {
		return fmt.Sprintf("n=%d err=%v (no EOF signal)", n, err)
	}
	if n == 0 {
		return "n=0 (EOF)"
	}
	return fmt.Sprintf("n=%d", n)
}

func eofWrite(typ int) string {
	fds, err := newPair(typ, 0)
	if err != nil {
		return "FAIL " + err.Error()
	}
	defer syscall.Close(fds[0])
	syscall.Close(fds[1])
	_, err = syscall.Write(fds[0], []byte("x"))
	if err == nil {
		return "write succeeded"
	}
	return "err=" + err.Error()
}

func pathTest(typ int) string {
	dir, err := os.MkdirTemp("", "upt")
	if err != nil {
		return err.Error()
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	srv, err := syscall.Socket(syscall.AF_UNIX, typ, 0)
	if err != nil {
		return "socket: FAIL " + err.Error()
	}
	defer syscall.Close(srv)
	if err := syscall.Bind(srv, &syscall.SockaddrUnix{Name: path}); err != nil {
		return "bind: FAIL " + err.Error()
	}
	if typ != syscall.SOCK_DGRAM {
		if err := syscall.Listen(srv, 8); err != nil {
			return "listen: FAIL " + err.Error()
		}
	}
	cli, err := syscall.Socket(syscall.AF_UNIX, typ, 0)
	if err != nil {
		return "client socket: FAIL " + err.Error()
	}
	defer syscall.Close(cli)
	if err := syscall.Connect(cli, &syscall.SockaddrUnix{Name: path}); err != nil {
		return "connect: FAIL " + err.Error()
	}
	peer := srv
	if typ != syscall.SOCK_DGRAM {
		p, _, err := syscall.Accept(srv)
		if err != nil {
			return "accept: FAIL " + err.Error()
		}
		defer syscall.Close(p)
		peer = p
	}
	msg := pattern(1500)
	if _, err := syscall.Write(cli, msg); err != nil {
		return "write: FAIL " + err.Error()
	}
	buf := make([]byte, 4096)
	n, err := syscall.Read(peer, buf)
	if err != nil {
		return "read: FAIL " + err.Error()
	}
	if typ != syscall.SOCK_STREAM && n != len(msg) {
		return fmt.Sprintf("read %d of %d", n, len(msg))
	}
	return "ok; " + wrongTypeConnect(path)
}

func wrongTypeConnect(path string) string {
	var parts []string
	for _, t := range transports {
		fd, err := syscall.Socket(syscall.AF_UNIX, t.typ, 0)
		if err != nil {
			continue
		}
		err = syscall.Connect(fd, &syscall.SockaddrUnix{Name: path})
		syscall.Close(fd)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s connect: %v", t.name, err))
		}
	}
	if len(parts) == 0 {
		return "every type connects"
	}
	return strings.Join(parts, ", ")
}

func throughput(typ, size, buf int) string {
	if typ != syscall.SOCK_STREAM && sendOne(typ, buf, size) != nil {
		return "skipped (message too large)"
	}
	fds, err := newPair(typ, buf)
	if err != nil {
		return "FAIL " + err.Error()
	}
	defer closePair(fds)
	count := throughputMB << 20 / size
	done := make(chan error, 1)
	go func() {
		msg := pattern(size)
		for i := 0; i < count; i++ {
			if err := writeAll(fds[0], msg); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	start := time.Now()
	rbuf := make([]byte, size)
	total := count * size
	got := 0
	for got < total {
		n, err := syscall.Read(fds[1], rbuf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return "read " + err.Error()
		}
		if n == 0 {
			break
		}
		got += n
	}
	elapsed := time.Since(start)
	if err := <-done; err != nil {
		return "write " + err.Error()
	}
	return fmt.Sprintf("%.0f MB/s, %.0f packets/s", float64(got)/elapsed.Seconds()/1e6, float64(count)/elapsed.Seconds())
}

func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(fd, b)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + n)
	}
	return b
}

func probeNet(network string) {
	dir, err := os.MkdirTemp("", "upt")
	if err != nil {
		fmt.Printf("%s: %v\n", network, err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	addr := &net.UnixAddr{Name: path, Net: network}
	if network == "unixgram" {
		srv, err := net.ListenUnixgram(network, addr)
		if err != nil {
			fmt.Printf("net.ListenUnixgram(%q): FAIL %v\n", network, err)
			return
		}
		defer srv.Close()
		cli, err := net.DialUnix(network, nil, addr)
		if err != nil {
			fmt.Printf("net.DialUnix(%q): FAIL %v\n", network, err)
			return
		}
		defer cli.Close()
		fmt.Printf("net %s: %s\n", network, netRoundTrip(cli, srv))
		return
	}
	l, err := net.ListenUnix(network, addr)
	if err != nil {
		fmt.Printf("net.ListenUnix(%q): FAIL %v\n", network, err)
		return
	}
	defer l.Close()
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	cli, err := net.DialUnix(network, nil, addr)
	if err != nil {
		fmt.Printf("net.DialUnix(%q): FAIL %v\n", network, err)
		return
	}
	defer cli.Close()
	srv := <-accepted
	if srv == nil {
		fmt.Printf("net %s: accept failed\n", network)
		return
	}
	defer srv.Close()
	fmt.Printf("net %s: %s\n", network, netRoundTrip(cli, srv))
}

func netRoundTrip(cli, srv *net.UnixConn) string {
	msg := pattern(1500)
	if _, err := cli.Write(msg); err != nil {
		return "write: FAIL " + err.Error()
	}
	_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := srv.Read(buf)
	if err != nil {
		return "read: FAIL " + err.Error()
	}
	f, err := srv.File()
	if err != nil {
		return fmt.Sprintf("ok (%d bytes), File(): %v", n, err)
	}
	f.Close()
	return fmt.Sprintf("ok (%d bytes), File() ok", n)
}
