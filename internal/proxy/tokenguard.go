package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (p *Proxy) tokenHostProvider(host string) (oauthProvider, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, o := range p.st.oauth {
		if o.tokenHost != "" && HostMatches([]string{o.tokenHost}, host) {
			return o, true
		}
	}
	return oauthProvider{}, false
}

func restrictTokenHostEncoding(h http.Header) {
	ae := h.Get("Accept-Encoding")
	if ae == "" {
		return
	}
	if strings.Contains(strings.ToLower(ae), "gzip") {
		h.Set("Accept-Encoding", "gzip")
		return
	}
	h.Set("Accept-Encoding", "identity")
}

type readCloser struct {
	io.Reader
	io.Closer
}

func (p *Proxy) guardTokenHostResponse(req *http.Request, resp *http.Response) error {
	prov, ok := p.tokenHostProvider(req.URL.Hostname())
	if !ok || !successStatus(resp.StatusCode) || req.Method == http.MethodHead || !bodyAllowedForStatus(resp.StatusCode) {
		return nil
	}
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if ce != "" && ce != "identity" && ce != "gzip" && ce != "x-gzip" {
		return fmt.Errorf("%s answered %s%s with Content-Encoding %q, which hsh cannot check for tokens; refusing to pass it to the sandbox", prov.name, req.URL.Host, req.URL.Path, ce)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, oauthBodyLimit+1))
	if err != nil {
		return err
	}
	if len(raw) > oauthBodyLimit {
		resp.Body = readCloser{io.MultiReader(bytes.NewReader(raw), resp.Body), resp.Body}
		return nil
	}
	resp.Body = readCloser{bytes.NewReader(raw), resp.Body}
	resp.ContentLength = int64(len(raw))
	resp.TransferEncoding = nil
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	body := raw
	if ce == "gzip" || ce == "x-gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("%s response from %s%s: gzip: %w", prov.name, req.URL.Host, req.URL.Path, err)
		}
		body, err = readLimited(zr, oauthBodyLimit)
		zr.Close()
		if err != nil {
			return fmt.Errorf("%s response from %s%s: gzip: %w", prov.name, req.URL.Host, req.URL.Path, err)
		}
	}
	if !isJSONBody(body, resp.Header.Get("Content-Type")) {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	for _, k := range tokenLikeFields {
		v, ok := doc[k].(string)
		if !ok || v == "" {
			continue
		}
		if _, known := p.store.lookup(v); known {
			continue
		}
		return fmt.Errorf("%s%s answered with %s, but that is not %s's token endpoint (%s); refusing to pass it to the sandbox (check token_endpoint.path in the provider descriptor: hsh oauth provider cat %s)", req.URL.Host, req.URL.Path, k, prov.name, prov.tokenPath, prov.name)
	}
	return nil
}
