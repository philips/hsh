package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	netstackMTU         = 65520
	netstackSniffWait   = 3 * time.Second
	netstackUDPIdle     = 30 * time.Second
	netstackMaxInFlight = 4096
)

func (p *Proxy) serveNetstack(path string) error {
	_ = os.Remove(path)
	l, err := net.Listen("unixpacket", path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", path, err)
	}
	log.Printf("netstack: waiting for the sandbox on %s", path)
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		log.Printf("netstack: sandbox connected")
		go func() {
			if err := p.runNetstack(c.(*net.UnixConn)); err != nil {
				log.Printf("netstack: %v", err)
			}
		}()
	}
}

func (p *Proxy) runNetstack(conn *net.UnixConn) error {
	f, err := conn.File()
	conn.Close()
	if err != nil {
		return err
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	closed := make(chan struct{})
	var once sync.Once
	ep, err := fdbased.New(&fdbased.Options{
		FDs: []int{int(f.Fd())},
		MTU: netstackMTU,
		ClosedFunc: func(tcpip.Error) {
			once.Do(func() { close(closed) })
		},
	})
	if err != nil {
		f.Close()
		return fmt.Errorf("link endpoint: %v", err)
	}
	if e := s.CreateNIC(1, ep); e != nil {
		f.Close()
		return fmt.Errorf("create NIC: %v", e)
	}
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: 1},
		{Destination: header.IPv6EmptySubnet, NIC: 1},
	})
	tf := tcp.NewForwarder(s, 0, netstackMaxInFlight, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		var wq waiter.Queue
		e, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		c := gonet.NewTCPConn(&wq, e)
		go p.handleTransparentTCP(c, id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)
	uf := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		if id.LocalPort != 53 {
			log.Printf("netstack: dropped udp to %s (only DNS is forwarded)", net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort))))
			return false
		}
		var wq waiter.Queue
		e, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			return false
		}
		go p.serveTransparentDNS(gonet.NewUDPConn(&wq, e), true)
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)
	<-closed
	log.Printf("netstack: sandbox disconnected")
	s.Close()
	s.Wait()
	f.Close()
	return nil
}

func (p *Proxy) serveTransparentDNS(c net.Conn, datagram bool) {
	defer c.Close()
	dc := &dns.Conn{Conn: c}
	w := &transparentDNSWriter{p: p, c: dc}
	for {
		if datagram {
			_ = c.SetReadDeadline(time.Now().Add(netstackUDPIdle))
		}
		m, err := dc.ReadMsg()
		if err != nil {
			return
		}
		if p.answerSynthetic(w, m) {
			continue
		}
		p.serveDNS(w, m, p.resolvers)
	}
}

type transparentDNSWriter struct {
	p *Proxy
	c *dns.Conn
}

func (w *transparentDNSWriter) LocalAddr() net.Addr  { return w.c.LocalAddr() }
func (w *transparentDNSWriter) RemoteAddr() net.Addr { return w.c.RemoteAddr() }
func (w *transparentDNSWriter) WriteMsg(m *dns.Msg) error {
	w.p.learnAnswers(m)
	return w.c.WriteMsg(m)
}
func (w *transparentDNSWriter) Write(b []byte) (int, error) { return w.c.Write(b) }
func (w *transparentDNSWriter) Close() error                { return nil }
func (w *transparentDNSWriter) TsigStatus() error           { return nil }
func (w *transparentDNSWriter) TsigTimersOnly(bool)         {}
func (w *transparentDNSWriter) Hijack()                     {}

type dnsName struct {
	name    string
	expires time.Time
}

var syntheticNet = netip.MustParsePrefix("198.18.0.0/15")

func (p *Proxy) isSynthetic(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && syntheticNet.Contains(a)
}

func (p *Proxy) answerSynthetic(w dns.ResponseWriter, r *dns.Msg) bool {
	if len(r.Question) != 1 || p.mode() == "https" {
		return false
	}
	q := r.Question[0]
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
		return false
	}
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	if p.allowListed(name) {
		return false
	}
	m := new(dns.Msg)
	m.SetReply(r)
	if q.Qtype == dns.TypeA {
		ip := p.syntheticFor(name)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1},
			A:   net.IP(ip.AsSlice()),
		})
	}
	log.Printf("dns: %s is not allowed; answered with a placeholder so the connection reaches the proxy", name)
	_ = w.WriteMsg(m)
	return true
}

func (p *Proxy) syntheticFor(name string) netip.Addr {
	p.namesMu.Lock()
	defer p.namesMu.Unlock()
	if p.names == nil {
		p.names = map[string]dnsName{}
	}
	if p.synthetic == nil {
		p.synthetic = map[string]netip.Addr{}
	}
	if ip, ok := p.synthetic[name]; ok {
		return ip
	}
	ip := syntheticNet.Addr().Next()
	for range len(p.synthetic) {
		ip = ip.Next()
	}
	p.synthetic[name] = ip
	p.names[ip.String()] = dnsName{name: name, expires: time.Now().Add(100 * 365 * 24 * time.Hour)}
	return ip
}

func (p *Proxy) learnAnswers(m *dns.Msg) {
	if m == nil || len(m.Question) == 0 {
		return
	}
	name := strings.ToLower(strings.TrimSuffix(m.Question[0].Name, "."))
	p.namesMu.Lock()
	defer p.namesMu.Unlock()
	if p.names == nil {
		p.names = map[string]dnsName{}
	}
	for _, rr := range m.Answer {
		var ip net.IP
		switch a := rr.(type) {
		case *dns.A:
			ip = a.A
		case *dns.AAAA:
			ip = a.AAAA
		default:
			continue
		}
		ttl := time.Duration(max(rr.Header().Ttl, 60)) * time.Second
		p.names[ip.String()] = dnsName{name: name, expires: time.Now().Add(ttl + 5*time.Minute)}
	}
}

func (p *Proxy) nameForIP(ip string) string {
	p.namesMu.Lock()
	defer p.namesMu.Unlock()
	n, ok := p.names[ip]
	if !ok || time.Now().After(n.expires) {
		return ""
	}
	return n.name
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (c *prefixConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

type sniffConn struct {
	net.Conn
	r io.Reader
}

func (c *sniffConn) Read(b []byte) (int, error)  { return c.r.Read(b) }
func (c *sniffConn) Write(b []byte) (int, error) { return 0, errSniffDone }

var errSniffDone = errors.New("sniffed")

func sniffSNI(c net.Conn) (string, []byte) {
	var buf bytes.Buffer
	sni := ""
	tc := tls.Server(&sniffConn{Conn: c, r: io.TeeReader(c, &buf)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = h.ServerName
			return nil, errSniffDone
		},
	})
	_ = tc.Handshake()
	return sni, buf.Bytes()
}

func looksLikeHTTP(b []byte) bool {
	for _, m := range []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE "} {
		if bytes.HasPrefix(b, []byte(m)) {
			return true
		}
	}
	return false
}

func (p *Proxy) handleTransparentTCP(c net.Conn, dstIP, dstPort string) {
	defer c.Close()
	if dstPort == "53" {
		p.serveTransparentDNS(c, false)
		return
	}
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(netstackSniffWait))
	first, err := br.Peek(1)
	if err == nil && first[0] == 0x16 {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		sni, hello := sniffSNI(&prefixConn{Conn: c, r: br})
		_ = c.SetReadDeadline(time.Time{})
		replay := &prefixConn{Conn: c, r: io.MultiReader(bytes.NewReader(hello), br)}
		p.transparentTLS(replay, sni, dstIP, dstPort)
		return
	}
	if err == nil {
		head, _ := br.Peek(8)
		if looksLikeHTTP(head) {
			_ = c.SetReadDeadline(time.Time{})
			p.transparentHTTP(&prefixConn{Conn: c, r: br}, br, dstIP, dstPort)
			return
		}
	}
	_ = c.SetReadDeadline(time.Time{})
	buffered, _ := br.Peek(br.Buffered())
	rest := io.MultiReader(bytes.NewReader(append([]byte(nil), buffered...)), c)
	p.transparentOpaque(&prefixConn{Conn: c, r: rest}, dstIP, dstPort)
}

func (p *Proxy) transparentAllowed(host, target, kind string, allowed func() bool) bool {
	if allowed() {
		return true
	}
	if !p.holdsEnabled() {
		log.Printf("denied %s (not in network.allow)", host)
		p.recordDenial(host, target)
		return false
	}
	return p.awaitApproval(context.Background(), host, target, kind, allowed, p.approvalWait())
}

func (p *Proxy) transparentTLS(c net.Conn, sni, dstIP, dstPort string) {
	host := sni
	if host == "" {
		host = p.nameForIP(dstIP)
	}
	dialHost := host
	if sni == "" && !p.isSynthetic(dstIP) {
		dialHost = dstIP
	}
	if host == "" {
		host = dstIP
	}
	target := net.JoinHostPort(host, dstPort)
	if !p.transparentAllowed(host, target, "TLS", func() bool { return p.connectAllowed(host, dstPort) }) {
		if sni != "" {
			p.denyTLS(c, sni)
		}
		return
	}
	secrets := p.secretsFor(host)
	if sni != "" && (len(secrets) > 0 || p.oauthInvolved(host)) {
		log.Printf("TLS %s -> allowed (intercept)", target)
		p.interceptTLS(c, host, target, secrets)
		return
	}
	log.Printf("TLS %s -> allowed (tunnel)", target)
	p.splice(c, net.JoinHostPort(dialHost, dstPort), target)
}

func (p *Proxy) denyTLS(c net.Conn, host string) {
	leaf, err := p.ca.Leaf(host)
	if err != nil {
		return
	}
	tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{leaf}, NextProtos: []string{"http/1.1"}})
	_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tc.Handshake(); err != nil {
		return
	}
	defer tc.Close()
	if _, err := http.ReadRequest(bufio.NewReader(tc)); err != nil {
		return
	}
	writeDenyResponse(tc, host)
}

func writeDenyResponse(w io.Writer, host string) {
	body := fmt.Sprintf(denialMessage, host)
	fmt.Fprintf(w, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (p *Proxy) transparentHTTP(c net.Conn, br *bufio.Reader, dstIP, dstPort string) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		host = dstIP
	}
	target := host
	if dstPort != "80" {
		target = net.JoinHostPort(host, dstPort)
	}
	if !p.transparentAllowed(host, target, req.Method, func() bool { return p.plaintextAllowed(host) }) {
		writeDenyResponse(c, host)
		return
	}
	p.serveRequests(c, br, req, "http", target, p.secretsFor(host))
}

func (p *Proxy) transparentOpaque(c net.Conn, dstIP, dstPort string) {
	host := p.nameForIP(dstIP)
	if host == "" {
		host = dstIP
	}
	target := net.JoinHostPort(host, dstPort)
	if !p.transparentAllowed(host, target, "TCP", func() bool { return p.opaqueAllowed(host) }) {
		return
	}
	log.Printf("TCP %s -> allowed (tunnel)", target)
	dial := dstIP
	if p.isSynthetic(dstIP) {
		dial = host
	}
	p.splice(c, net.JoinHostPort(dial, dstPort), target)
}

func (p *Proxy) opaqueAllowed(host string) bool {
	return p.mode() != "none" && p.allowListed(host)
}

func (p *Proxy) splice(c net.Conn, dial, target string) {
	up, err := net.DialTimeout("tcp", dial, 15*time.Second)
	if err != nil {
		log.Printf("%s: upstream dial failed: %v", target, err)
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, c)
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, up)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
