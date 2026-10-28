package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"hsh/internal/certs"
	"hsh/internal/config"
	"hsh/internal/denieddb"
)

type Options struct {
	ListenAddr   string
	ConfigPath   string
	SecretsPath  string
	CACertPath   string
	CAKeyPath    string
	RootsPath    string
	DeniedDB     string
	OAuthStore   string
	NetworkMode  string
	Session      string
	Workspace    string
	AttendedPath string
}

type state struct {
	allow        []string
	approvalWait time.Duration
	mode         string
	secrets      []config.ResolvedSecret
	oauth        []oauthProvider
}

type Proxy struct {
	opts      Options
	ca        *certs.CA
	roots     *x509.CertPool
	transport *http.Transport
	deniedDB  *sql.DB
	store     *tokenStore
	holds     holdState

	mu sync.RWMutex
	st state
}

func New(opts Options) (*Proxy, error) {
	certPEM, err := os.ReadFile(opts.CACertPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(opts.CAKeyPath)
	if err != nil {
		return nil, err
	}
	ca, err := certs.LoadCA(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if opts.RootsPath != "" {
		if data, err := os.ReadFile(opts.RootsPath); err == nil {
			roots.AppendCertsFromPEM(data)
		}
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{RootCAs: roots},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	var db *sql.DB
	if opts.DeniedDB != "" {
		var err error
		db, err = denieddb.Open(opts.DeniedDB)
		if err != nil {
			return nil, fmt.Errorf("open denial database: %w", err)
		}
		if opts.Session != "" {
			_ = denieddb.ResetHolding(db, opts.Session)
		}
	}
	return &Proxy{opts: opts, ca: ca, roots: roots, transport: transport, deniedDB: db, store: loadTokenStore(opts.OAuthStore)}, nil
}

func (p *Proxy) Run() error {
	if err := p.reload(); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	p.mu.RLock()
	mode := p.st.mode
	allowN, secretN := len(p.st.allow), len(p.st.secrets)
	p.mu.RUnlock()
	log.Printf("configuration loaded: network mode %s, %d allowlist entries, %d secret(s)", mode, allowN, secretN)
	go p.watch(1 * time.Second)
	var listener net.Listener
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for {
		listener, err = net.Listen("tcp", p.opts.ListenAddr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("listen on %s: %w", p.opts.ListenAddr, err)
		}
		log.Printf("waiting for %s to become available: %v", p.opts.ListenAddr, err)
		time.Sleep(200 * time.Millisecond)
	}
	log.Printf("egress proxy listening on %s", p.opts.ListenAddr)
	dnsAddr := p.opts.ListenAddr
	if host, _, err := net.SplitHostPort(dnsAddr); err == nil {
		dnsAddr = net.JoinHostPort(host, "53")
	}
	go p.startDNS(dnsAddr)
	server := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	return server.Serve(listener)
}

func (p *Proxy) reload() error {
	cfg, err := config.Load(p.opts.ConfigPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p.opts.SecretsPath)
	if err != nil {
		return err
	}
	var rs config.ResolvedSecrets
	if err := json.Unmarshal(data, &rs); err != nil {
		return err
	}
	p.mu.Lock()
	p.st.allow = cfg.Network.Allow
	p.st.approvalWait = cfg.Network.ApprovalWaitDuration()
	p.st.mode = p.opts.NetworkMode
	if p.st.mode == "" {
		p.st.mode = cfg.Network.ModeOrDefault()
	}
	p.st.secrets = rs.Secrets
	p.st.oauth = oauthProviders(cfg.OAuth)
	p.mu.Unlock()
	return nil
}

func (p *Proxy) watch(interval time.Duration) {
	lastCfg, _ := os.ReadFile(p.opts.ConfigPath)
	lastSec, _ := os.ReadFile(p.opts.SecretsPath)
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		cfgData, err1 := os.ReadFile(p.opts.ConfigPath)
		secData, err2 := os.ReadFile(p.opts.SecretsPath)
		if err1 != nil || err2 != nil {
			continue
		}
		if bytes.Equal(lastCfg, cfgData) && bytes.Equal(lastSec, secData) {
			continue
		}
		lastCfg, lastSec = cfgData, secData
		if err := p.reload(); err != nil {
			log.Printf("configuration reload failed: %v", err)
		} else {
			p.mu.RLock()
			mode, allowN, secretN := p.st.mode, len(p.st.allow), len(p.st.secrets)
			p.mu.RUnlock()
			log.Printf("configuration reloaded: network mode %s, %d allowlist entries, %d secret(s)", mode, allowN, secretN)
		}
	}
}

func (p *Proxy) mode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st.mode
}

func (p *Proxy) allowListed(host string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return HostMatches(p.st.allow, host)
}

func (p *Proxy) plaintextAllowed(host string) bool {
	return p.mode() != "none" && p.allowListed(host)
}

func (p *Proxy) connectAllowed(host, port string) bool {
	switch p.mode() {
	case "none":
		return false
	case "https":
		return port == "443" || p.allowListed(host)
	default:
		return p.allowListed(host)
	}
}

func (p *Proxy) secretsFor(host string) []config.ResolvedSecret {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []config.ResolvedSecret
	for _, s := range p.st.secrets {
		if HostMatches(s.Hosts, host) {
			out = append(out, s)
		}
	}
	return out
}

func HostMatches(list []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range list {
		e := strings.ToLower(strings.TrimSuffix(entry, "."))
		if e == host {
			return true
		}
		if strings.HasPrefix(e, "*.") {
			suffix := e[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "hsh proxy: expected an absolute-URI proxy request", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	if !p.gate(w, r.Context(), host, r.URL.Host, r.Method, func() bool { return p.plaintextAllowed(host) }) {
		return
	}
	if prov := p.oauthTokenEndpoint(host, r.URL.Path); prov != nil && r.Method == http.MethodPost {
		log.Printf("%s %s%s -> oauth token exchange (%s)", r.Method, r.URL.Host, r.URL.RequestURI(), prov.name)
		p.oauthServeHTTP(w, r, prov)
		return
	}
	p.redeemBearerHeader(r.Header, host)
	for _, s := range p.secretsFor(host) {
		r.Header.Set(s.Header, s.Prefix+s.Value)
		log.Printf("%s %s%s: injecting header %s", r.Method, r.URL.Host, r.URL.RequestURI(), s.Header)
	}
	log.Printf("%s %s%s -> allowed", r.Method, r.URL.Host, r.URL.RequestURI())
	p.forwardHTTP(w, r)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	port := "443"
	if h, p2, err := net.SplitHostPort(r.Host); err == nil {
		host = h
		if p2 != "" {
			port = p2
		}
	}
	if !p.gate(w, r.Context(), host, r.Host, "CONNECT", func() bool { return p.connectAllowed(host, port) }) {
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hsh proxy: connection hijacking unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		log.Printf("hijack: %v", err)
		return
	}
	defer clientConn.Close()
	upstream, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		log.Printf("CONNECT %s: upstream dial failed: %v", r.Host, err)
		clientBuf.WriteString("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		clientBuf.Flush()
		return
	}
	secrets := p.secretsFor(host)
	if len(secrets) == 0 && !p.oauthInvolved(host) {
		log.Printf("CONNECT %s -> allowed (tunnel)", r.Host)
		p.tunnel(clientConn, clientBuf, upstream)
		return
	}
	upstream.Close()
	for _, s := range secrets {
		log.Printf("CONNECT %s: will inject header %s", r.Host, s.Header)
	}
	if len(secrets) == 0 {
		log.Printf("CONNECT %s: will intercept for oauth", r.Host)
	} else {
		log.Printf("CONNECT %s -> allowed (intercept)", r.Host)
	}
	p.intercept(clientConn, clientBuf, host, r.Host, secrets)
}

func (p *Proxy) tunnel(clientConn net.Conn, clientBuf *bufio.ReadWriter, upstream net.Conn) {
	defer upstream.Close()
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := clientBuf.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientBuf)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientConn, upstream)
		done <- struct{}{}
	}()
	<-done
}

func (p *Proxy) intercept(clientConn net.Conn, clientBuf *bufio.ReadWriter, host, target string, secrets []config.ResolvedSecret) {
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := clientBuf.Flush(); err != nil {
		return
	}
	leaf, err := p.ca.Leaf(host)
	if err != nil {
		log.Printf("generate certificate for %s: %v", host, err)
		return
	}
	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{leaf},
		NextProtos:   []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("TLS handshake with client for %s: %v", host, err)
		return
	}
	defer tlsConn.Close()
	p.serveConn(tlsConn, target, secrets)
}

func (p *Proxy) serveConn(conn *tls.Conn, target string, secrets []config.ResolvedSecret) {
	br := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		req.URL.Scheme = "https"
		req.URL.Host = target
		host := req.URL.Hostname()
		if prov := p.oauthTokenEndpoint(host, req.URL.Path); prov != nil && req.Method == http.MethodPost {
			log.Printf("%s %s%s -> oauth token exchange (%s)", req.Method, target, req.URL.RequestURI(), prov.name)
			p.oauthServeConn(conn, req, prov)
			if req.Close {
				return
			}
			continue
		}
		p.redeemBearerHeader(req.Header, host)
		for _, s := range secrets {
			req.Header.Set(s.Header, s.Prefix+s.Value)
			log.Printf("%s %s%s: injecting header %s", req.Method, target, req.URL.RequestURI(), s.Header)
		}
		log.Printf("%s %s%s -> allowed", req.Method, target, req.URL.RequestURI())
		p.forwardRequest(conn, req)
		if req.Close {
			return
		}
	}
}

func (p *Proxy) forwardRequest(w io.Writer, req *http.Request) {
	removeHopHeaders(req.Header)
	req.RequestURI = ""
	req.Close = false
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		log.Printf("upstream %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		fmt.Fprint(w, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	if resp.ContentLength >= 0 {
		resp.Proto = "HTTP/1.1"
		resp.ProtoMajor = 1
		resp.ProtoMinor = 1
		if err := resp.Write(w); err != nil {
			log.Printf("write response for %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		}
		return
	}
	resp.Header.Del("Content-Length")
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n", resp.Status)
	if err := resp.Header.Write(w); err != nil {
		log.Printf("write response headers for %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		return
	}
	if req.Method == http.MethodHead || !bodyAllowedForStatus(resp.StatusCode) {
		fmt.Fprint(w, "\r\n")
		return
	}
	fmt.Fprint(w, "Transfer-Encoding: chunked\r\n\r\n")
	cw := httputil.NewChunkedWriter(w)
	if _, err := io.Copy(cw, resp.Body); err != nil {
		log.Printf("stream response for %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		return
	}
	if err := cw.Close(); err != nil {
		log.Printf("finish chunked response for %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		return
	}
	fmt.Fprint(w, "\r\n")
}

func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status < 200:
		return false
	case status == http.StatusNoContent:
		return false
	case status == http.StatusNotModified:
		return false
	}
	return true
}

func (p *Proxy) forwardHTTP(w http.ResponseWriter, r *http.Request) {
	removeHopHeaders(r.Header)
	r.RequestURI = ""
	r.Close = false
	resp, err := p.transport.RoundTrip(r)
	if err != nil {
		log.Printf("upstream %s: %v", r.URL.Host, err)
		http.Error(w, "hsh proxy: upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func removeHopHeaders(h http.Header) {
	for _, k := range hopHeaders {
		h.Del(k)
	}
}
