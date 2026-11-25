package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"z.gfire.dev/gvx/pkg/buffer"
	"z.gfire.dev/gvx/pkg/tcpip"
	"z.gfire.dev/gvx/pkg/tcpip/adapters/gonet"
	"z.gfire.dev/gvx/pkg/tcpip/header"
	"z.gfire.dev/gvx/pkg/tcpip/link/channel"
	"z.gfire.dev/gvx/pkg/tcpip/network/ipv4"
	"z.gfire.dev/gvx/pkg/tcpip/network/ipv6"
	"z.gfire.dev/gvx/pkg/tcpip/stack"
	"z.gfire.dev/gvx/pkg/tcpip/transport/tcp"
	"z.gfire.dev/gvx/pkg/tcpip/transport/udp"
	"z.gfire.dev/gvx/pkg/waiter"
)

const (
	bulkPort = 81
	sinkPort = 82
)

type peer struct {
	mtu      uint32
	resolver string
	bulkMB   int
}

func runPeer(args []string) int {
	fs := flag.NewFlagSet("peer", flag.ExitOnError)
	listen := fs.String("listen", "", "Unix stream socket path to listen on")
	mtu := fs.Uint("mtu", 65520, "link MTU")
	resolver := fs.String("resolver", "", "upstream DNS server host:port (default: first nameserver in /etc/resolv.conf)")
	bulkMB := fs.Int("bulk-mb", 256, "MiB that the peer sends to a connection to port 81")
	_ = fs.Parse(args)

	p := &peer{mtu: uint32(*mtu), resolver: *resolver, bulkMB: *bulkMB}
	if p.resolver == "" {
		p.resolver = hostResolver()
	}
	_ = os.Remove(*listen)
	l, err := net.Listen("unix", *listen)
	if err != nil {
		fail("listen", err)
	}
	peerLog("listening on %s (mtu %d, resolver %s, port %d sends %d MiB, port %d discards)", *listen, p.mtu, p.resolver, bulkPort, p.bulkMB, sinkPort)
	for {
		c, err := l.Accept()
		if err != nil {
			fail("accept", err)
		}
		peerLog("sandbox connected")
		go p.serve(c)
	}
}

func hostResolver() string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" {
				return net.JoinHostPort(f[1], "53")
			}
		}
	}
	return "1.1.1.1:53"
}

func peerLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "peer %s: "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

func (p *peer) serve(conn net.Conn) {
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.SetReadBuffer(4 << 20)
		_ = uc.SetWriteBuffer(4 << 20)
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	ep := channel.New(1024, p.mtu, "")
	if err := s.CreateNIC(1, ep); err != nil {
		peerLog("create NIC: %v", err)
		return
	}
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: 1},
		{Destination: header.IPv6EmptySubnet, NIC: 1},
	})
	sack := tcpip.TCPSACKEnabled(true)
	_ = s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	tf := tcp.NewForwarder(s, 0, 4096, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		var wq waiter.Queue
		e, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		go p.handleTCP(gonet.NewTCPConn(&wq, e), net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort))))
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)
	uf := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		dst := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))
		if id.LocalPort != 53 {
			peerLog("udp %s dropped", dst)
			return false
		}
		var wq waiter.Queue
		e, err := r.CreateEndpoint(&wq)
		if err != nil {
			return false
		}
		go p.handleDNS(gonet.NewUDPConn(&wq, e), dst)
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var hdr [4]byte
		for {
			pkt := ep.ReadContext(ctx)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			pkt.DecRef()
			binary.BigEndian.PutUint32(hdr[:], uint32(v.Size()))
			bufs := net.Buffers{hdr[:], v.AsSlice()}
			_, err := bufs.WriteTo(conn)
			v.Release()
			if err != nil {
				return
			}
		}
	}()
	r := bufio.NewReaderSize(conn, 256<<10)
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			peerLog("sandbox disconnected: %v", err)
			break
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > 65535 {
			peerLog("bad frame length %d", n)
			break
		}
		v := buffer.NewViewSize(int(n))
		if _, err := io.ReadFull(r, v.AsSlice()); err != nil {
			v.Release()
			peerLog("read frame: %v", err)
			break
		}
		var proto tcpip.NetworkProtocolNumber
		switch header.IPVersion(v.AsSlice()) {
		case header.IPv4Version:
			proto = ipv4.ProtocolNumber
		case header.IPv6Version:
			proto = ipv6.ProtocolNumber
		default:
			v.Release()
			continue
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithView(v)})
		ep.InjectInbound(proto, pkt)
		pkt.DecRef()
	}
	cancel()
	_ = conn.Close()
	ep.Close()
	wg.Wait()
	s.Close()
	s.Wait()
}

func (p *peer) handleTCP(c net.Conn, dst string) {
	defer c.Close()
	start := time.Now()
	_, port, _ := net.SplitHostPort(dst)
	if port == strconv.Itoa(sinkPort) {
		n, _ := io.Copy(io.Discard, c)
		el := time.Since(start)
		peerLog("tcp %s sink: received %d MiB in %s (%.0f MB/s)", dst, n>>20, el.Round(time.Millisecond), float64(n)/el.Seconds()/1e6)
		return
	}
	if port == strconv.Itoa(bulkPort) {
		chunk := make([]byte, 64<<10)
		total := int64(p.bulkMB) << 20
		var sent int64
		for sent < total {
			n, err := c.Write(chunk)
			sent += int64(n)
			if err != nil {
				break
			}
		}
		el := time.Since(start)
		peerLog("tcp %s bulk: sent %d MiB in %s (%.0f MB/s)", dst, sent>>20, el.Round(time.Millisecond), float64(sent)/el.Seconds()/1e6)
		return
	}
	up, err := net.DialTimeout("tcp", dst, 10*time.Second)
	if err != nil {
		peerLog("tcp %s: dial: %v", dst, err)
		return
	}
	defer up.Close()
	var wg sync.WaitGroup
	var upBytes, downBytes int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		upBytes, _ = io.Copy(up, c)
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		downBytes, _ = io.Copy(c, up)
		if tc, ok := c.(*gonet.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
	peerLog("tcp %s: up %d B, down %d B, %s", dst, upBytes, downBytes, time.Since(start).Round(time.Millisecond))
}

func (p *peer) handleDNS(c net.Conn, dst string) {
	defer c.Close()
	up, err := net.Dial("udp", p.resolver)
	if err != nil {
		peerLog("dns %s: dial %s: %v", dst, p.resolver, err)
		return
	}
	defer up.Close()
	buf := make([]byte, 65535)
	for {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		peerLog("dns query to %s: %s", dst, dnsName(buf[:n]))
		if _, err := up.Write(buf[:n]); err != nil {
			return
		}
		_ = up.SetReadDeadline(time.Now().Add(5 * time.Second))
		m, err := up.Read(buf)
		if err != nil {
			peerLog("dns %s: upstream: %v", dst, err)
			return
		}
		if _, err := c.Write(buf[:m]); err != nil {
			return
		}
	}
}

func dnsName(msg []byte) string {
	if len(msg) < 13 {
		return "?"
	}
	var labels []string
	for i := 12; i < len(msg); {
		l := int(msg[i])
		if l == 0 || i+1+l > len(msg) {
			break
		}
		labels = append(labels, string(msg[i+1:i+1+l]))
		i += 1 + l
	}
	return strings.Join(labels, ".")
}
