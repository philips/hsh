package proxy

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hsh/internal/config"
)

type oauthProvider struct {
	name        string
	tokenHost   string
	tokenPath   string
	fields      []string
	nonceFields []string
	apiHosts    []string
}

func oauthProviders(cfg map[string]config.OAuthProvider) []oauthProvider {
	var out []oauthProvider
	for name, p := range cfg {
		fields := p.ResponseFields
		if len(fields) == 0 {
			fields = []string{"access_token", "refresh_token"}
		}
		out = append(out, oauthProvider{
			name:        name,
			tokenHost:   p.TokenEndpoint.Host,
			tokenPath:   p.TokenEndpoint.Path,
			fields:      fields,
			nonceFields: p.RequestNonceFields,
			apiHosts:    p.APIHosts,
		})
	}
	return out
}

func (p *oauthProvider) matchesTokenEndpoint(host, path string) bool {
	return p.tokenHost != "" && HostMatches([]string{p.tokenHost}, host) && p.tokenPath == path
}

func (p *oauthProvider) matchesAPIHost(host string) bool {
	return len(p.apiHosts) > 0 && HostMatches(p.apiHosts, host)
}

type storedToken struct {
	Provider string `json:"provider"`
	Field    string `json:"field"`
	Value    string `json:"value"`
	Created  string `json:"created"`
}

type tokenStore struct {
	mu   sync.Mutex
	path string
	m    map[string]storedToken
}

func loadTokenStore(path string) *tokenStore {
	s := &tokenStore{path: path, m: map[string]storedToken{}}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s.m)
	return s
}

func (s *tokenStore) lookup(phantom string) (storedToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[phantom]
	return t, ok
}

func (s *tokenStore) put(phantom string, t storedToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Created = time.Now().UTC().Format(time.RFC3339)
	s.m[phantom] = t
	if s.path == "" {
		return
	}
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("oauth: writing the token store %s: %v (captured tokens will not resolve after a restart)", s.path, err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("oauth: renaming the token store %s: %v", s.path, err)
	}
}

func newPhantom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "hsh_" + hex.EncodeToString(b), nil
}

func (p *Proxy) oauthTokenEndpoint(host, path string) *oauthProvider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.st.oauth {
		if p.st.oauth[i].matchesTokenEndpoint(host, path) {
			return &p.st.oauth[i]
		}
	}
	return nil
}

func (p *Proxy) oauthAPIHost(host string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.st.oauth {
		if p.st.oauth[i].matchesAPIHost(host) {
			return true
		}
	}
	return false
}

func (p *Proxy) oauthInvolved(host string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.st.oauth {
		o := &p.st.oauth[i]
		if o.matchesAPIHost(host) || (o.tokenHost != "" && HostMatches([]string{o.tokenHost}, host)) {
			return true
		}
	}
	return false
}

func (p *Proxy) redeemBearerHeader(h http.Header, host string) bool {
	if !p.oauthAPIHost(host) {
		return false
	}
	auth := h.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer hsh_") {
		return false
	}
	phantom := strings.TrimPrefix(auth, "Bearer ")
	t, ok := p.store.lookup(phantom)
	if !ok {
		log.Printf("oauth: unknown phantom presented for %s; passing it through unresolved (is the oauth store missing the mapping?)", host)
		return false
	}
	h.Set("Authorization", "Bearer "+t.Value)
	log.Printf("oauth: redeemed %s %s token for %s", t.Provider, t.Field, host)
	return true
}

const oauthBodyLimit = 1 << 20

func oauthFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func logExchangeRequest(prov *oauthProvider, body []byte) {
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		log.Printf("oauth: %s exchange request is not form-encoded; request fields not logged", prov.name)
		return
	}
	parts := []string{"grant_type=" + vals.Get("grant_type")}
	if c := vals.Get("code"); c != "" {
		parts = append(parts, fmt.Sprintf("code=%s(len %d)", oauthFingerprint(c), len(c)))
	}
	if v := vals.Get("code_verifier"); v != "" {
		parts = append(parts, fmt.Sprintf("code_verifier(len %d)", len(v)))
	}
	if r := vals.Get("redirect_uri"); r != "" {
		parts = append(parts, "redirect_uri="+r)
	}
	if id := vals.Get("client_id"); id != "" {
		parts = append(parts, "client_id="+id)
	}
	if rt := vals.Get("refresh_token"); rt != "" {
		parts = append(parts, fmt.Sprintf("refresh_token=%s(len %d)", oauthFingerprint(rt), len(rt)))
	}
	log.Printf("oauth: %s exchange request: %s", prov.name, strings.Join(parts, " "))
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (p *Proxy) redeemRequestBody(body []byte, prov *oauthProvider) []byte {
	if len(prov.nonceFields) == 0 || len(body) == 0 {
		return body
	}
	ct := http.DetectContentType(body)
	if strings.Contains(ct, "json") {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return body
		}
		changed := false
		for _, f := range prov.nonceFields {
			if v, ok := doc[f].(string); ok {
				if t, found := p.store.lookup(v); found {
					doc[f] = t.Value
					changed = true
					log.Printf("oauth: redeemed %s %s nonce for token exchange", t.Provider, t.Field)
				}
			}
		}
		if !changed {
			return body
		}
		out, err := json.Marshal(doc)
		if err != nil {
			return body
		}
		return out
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return body
	}
	changed := false
	for _, f := range prov.nonceFields {
		for i, v := range vals[f] {
			if t, found := p.store.lookup(v); found {
				vals[f][i] = t.Value
				changed = true
				log.Printf("oauth: redeemed %s %s nonce for token exchange", t.Provider, t.Field)
			}
		}
	}
	if !changed {
		return body
	}
	return []byte(vals.Encode())
}

func (p *Proxy) captureTokenResponse(body []byte, prov *oauthProvider) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		log.Printf("oauth: %s token response is not JSON; passing it through unchanged (check token_endpoint path and content type)", prov.name)
		return body
	}
	changed := false
	for _, f := range prov.fields {
		real, ok := doc[f].(string)
		if !ok || real == "" {
			continue
		}
		if strings.HasPrefix(real, "hsh_") {
			continue
		}
		phantom, err := newPhantom()
		if err != nil {
			continue
		}
		p.store.put(phantom, storedToken{Provider: prov.name, Field: f, Value: real})
		doc[f] = phantom
		changed = true
		log.Printf("oauth: captured %s %s (sandbox sees a phantom)", prov.name, f)
	}
	if !changed {
		log.Printf("oauth: %s token response contained none of the declared fields %v; passing it through unchanged", prov.name, prov.fields)
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func oauthErrorSummary(body []byte) string {
	var doc struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &doc); err == nil {
		switch {
		case doc.Error != "" && doc.ErrorDescription != "":
			return doc.Error + ": " + doc.ErrorDescription
		case doc.Error != "":
			return doc.Error
		case doc.ErrorDescription != "":
			return doc.ErrorDescription
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func (p *Proxy) oauthExchange(req *http.Request, prov *oauthProvider) (*http.Response, []byte, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = readLimited(req.Body, oauthBodyLimit)
		if err != nil {
			return nil, nil, err
		}
		req.Body.Close()
	}
	logExchangeRequest(prov, body)
	body = p.redeemRequestBody(body, prov)
	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	} else {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	removeHopHeaders(req.Header)
	req.RequestURI = ""
	req.Close = false
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := readLimited(resp.Body, oauthBodyLimit)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("oauth: %s token exchange: upstream %s (%s, %d bytes)", prov.name, resp.Status, resp.Header.Get("Content-Type"), len(respBody))
	if resp.StatusCode >= 400 {
		log.Printf("oauth: token endpoint refused the exchange: %s", oauthErrorSummary(respBody))
	}
	respBody = p.captureTokenResponse(respBody, prov)
	return resp, respBody, nil
}

func (p *Proxy) oauthServeHTTP(w http.ResponseWriter, req *http.Request, prov *oauthProvider) {
	resp, body, err := p.oauthExchange(req, prov)
	if err != nil {
		log.Printf("oauth: token exchange failed: %v", err)
		http.Error(w, "hsh proxy: oauth token exchange failed", http.StatusBadGateway)
		return
	}
	removeHopHeaders(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (p *Proxy) oauthServeConn(w io.Writer, req *http.Request, prov *oauthProvider) {
	resp, body, err := p.oauthExchange(req, prov)
	if err != nil {
		log.Printf("oauth: token exchange failed: %v", err)
		fmt.Fprint(w, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	removeHopHeaders(resp.Header)
	resp.Proto = "HTTP/1.1"
	resp.ProtoMajor = 1
	resp.ProtoMinor = 1
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	if err := resp.Write(w); err != nil {
		log.Printf("oauth: write token response: %v", err)
	}
}
