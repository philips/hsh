package proxy

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"hsh/internal/monitordb"
)

type byteCounter struct {
	up, down atomic.Int64
}

type countingConn struct {
	net.Conn
	n *byteCounter
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n.up.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.n.down.Add(int64(n))
	return n, err
}

func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c *countingBody) Read(b []byte) (int, error) {
	n, err := c.ReadCloser.Read(b)
	c.n.Add(int64(n))
	return n, err
}

type countingResponseWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n.Add(int64(n))
	return n, err
}

func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking unsupported")
}

type connRecord struct {
	p      *Proxy
	bytes  *byteCounter
	start  time.Time
	mu     sync.Mutex
	c      monitordb.Connection
	logged bool
	ended  bool
}

func (p *Proxy) newRecord(host, port, scheme string, bytes *byteCounter) *connRecord {
	if bytes == nil {
		bytes = &byteCounter{}
	}
	n, _ := strconv.Atoi(port)
	return &connRecord{
		p: p, bytes: bytes, start: time.Now(),
		c: monitordb.Connection{
			Time: time.Now().UTC().Format(time.RFC3339), Host: host, Port: n, Scheme: scheme,
			Access: monitordb.AccessAllow, Log: monitordb.LogRecord,
		},
	}
}

func (r *connRecord) decided(access, logLevel string, rule monitordb.Rule) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.c.Access, r.c.Log = access, logLevel
	r.c.Pattern, r.c.Source = rule.Pattern, rule.Source
}

func (r *connRecord) held(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.c.HeldMS = d.Milliseconds()
	r.mu.Unlock()
}

func (r *connRecord) write() {
	if r == nil || r.p.connLog == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.c.BytesUp, r.c.BytesDown = r.bytes.up.Load(), r.bytes.down.Load()
	r.c.DurationMS = time.Since(r.start).Milliseconds()
	if r.logged {
		if err := r.p.connLog.Update(r.c); err != nil {
			log.Printf("connection log: %v", err)
		}
		return
	}
	id, err := r.p.connLog.Insert(r.c)
	if err != nil {
		log.Printf("connection log: %v", err)
		return
	}
	r.c.ID, r.logged = id, true
}

func (r *connRecord) finish() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return
	}
	r.ended = true
	r.mu.Unlock()
	r.write()
}
