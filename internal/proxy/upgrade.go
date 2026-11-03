package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

func upgradeProtocol(h http.Header) (string, bool) {
	up := strings.TrimSpace(h.Get("Upgrade"))
	if up == "" {
		return "", false
	}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return up, true
			}
		}
	}
	return "", false
}

func (p *Proxy) forwardUpgrade(w io.Writer, clientR io.Reader, req *http.Request, protocol string, onStatus func(int)) {
	removeHopHeaders(req.Header)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", protocol)
	req.RequestURI = ""
	req.Close = false
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		log.Printf("upstream %s%s (upgrade to %s): %v", req.URL.Host, req.URL.RequestURI(), protocol, err)
		fmt.Fprint(w, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	if onStatus != nil {
		onStatus(resp.StatusCode)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		log.Printf("%s%s: upstream refused the upgrade to %s: %s", req.URL.Host, req.URL.RequestURI(), protocol, resp.Status)
		defer resp.Body.Close()
		removeHopHeaders(resp.Header)
		resp.Header.Set("Connection", "close")
		resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
		resp.Close = true
		bw := bufio.NewWriterSize(w, 4096)
		if err := resp.Write(bw); err != nil {
			log.Printf("write response for %s%s: %v", req.URL.Host, req.URL.RequestURI(), err)
		}
		_ = bw.Flush()
		return
	}
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		log.Printf("%s%s: upgraded connection is not writable", req.URL.Host, req.URL.RequestURI())
		fmt.Fprint(w, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer upstream.Close()
	head := &bytes.Buffer{}
	fmt.Fprintf(head, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	if err := resp.Header.Write(head); err != nil {
		return
	}
	head.WriteString("\r\n")
	if _, err := w.Write(head.Bytes()); err != nil {
		return
	}
	if rw, ok := w.(*bufio.ReadWriter); ok {
		_ = rw.Writer.Flush()
	}
	log.Printf("%s%s: upgraded to %s; relaying", req.URL.Host, req.URL.RequestURI(), protocol)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientR)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(w, upstream)
		done <- struct{}{}
	}()
	<-done
}

func (p *Proxy) forwardUpgradeHTTP(w http.ResponseWriter, r *http.Request, protocol string, onStatus func(int)) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hsh proxy: connection upgrade unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		log.Printf("hijack for upgrade: %v", err)
		return
	}
	defer conn.Close()
	p.forwardUpgrade(conn, buf, r, protocol, onStatus)
}
