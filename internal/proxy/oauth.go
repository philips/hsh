package proxy

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hsh/internal/oauthprov"
)

type oauthProvider struct {
	name        string
	tokenHost   string
	tokenPath   string
	fields      []oauthprov.Field
	nonceFields []string
	apiHosts    []string
	header      string
	prefix      string
}

func oauthProviders(resolved []oauthprov.Resolved) []oauthProvider {
	var out []oauthProvider
	for _, r := range resolved {
		out = append(out, oauthProvider{
			name:        r.Name,
			tokenHost:   r.TokenEndpoint.Host,
			tokenPath:   r.TokenEndpoint.Path,
			fields:      r.ResponseFields,
			nonceFields: r.RequestNonceFields,
			apiHosts:    r.APIHosts,
			header:      r.Header,
			prefix:      r.Prefix,
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

func (p *oauthProvider) fieldNames() []string {
	out := make([]string, 0, len(p.fields))
	for _, f := range p.fields {
		out = append(out, f.Path)
	}
	return out
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

func jwtPhantom(real, marker string) (string, bool) {
	parts := strings.Split(real, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	return parts[0] + "." + parts[1] + "." + marker, true
}

func presentedPhantom(f oauthprov.Field, real string) (string, error) {
	marker, err := newPhantom()
	if err != nil {
		return "", err
	}
	if f.Kind == "jwt" {
		if ph, ok := jwtPhantom(real, marker); ok {
			return ph, nil
		}
		log.Printf("oauth: %s is declared as a JWT but the value is not one; the sandbox gets an opaque phantom", f.Path)
	}
	if f.Format != "" {
		return strings.Replace(f.Format, oauthprov.PhantomPlaceholder, marker, 1), nil
	}
	return marker, nil
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

func (p *Proxy) oauthAPIProviders(host string) []oauthProvider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []oauthProvider
	for i := range p.st.oauth {
		if p.st.oauth[i].matchesAPIHost(host) {
			out = append(out, p.st.oauth[i])
		}
	}
	return out
}

func (p *Proxy) redeemBearerHeader(h http.Header, host string) bool {
	for _, prov := range p.oauthAPIProviders(host) {
		value := h.Get(prov.header)
		if !strings.HasPrefix(value, prov.prefix) {
			continue
		}
		candidate := strings.TrimPrefix(value, prov.prefix)
		if !strings.Contains(candidate, "hsh_") {
			continue
		}
		t, ok := p.store.lookup(candidate)
		if !ok {
			log.Printf("oauth: unknown phantom presented for %s; passing it through unresolved (is the oauth store missing the mapping?)", host)
			continue
		}
		if t.Provider != prov.name {
			continue
		}
		h.Set(prov.header, prov.prefix+t.Value)
		log.Printf("oauth: redeemed %s %s token for %s", t.Provider, t.Field, host)
		return true
	}
	return false
}

const oauthBodyLimit = 1 << 20

func oauthFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func requestFields(body []byte, contentType string) (map[string]string, bool) {
	if isJSONBody(body, contentType) {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, false
		}
		out := map[string]string{}
		for k, v := range doc {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out, true
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for k := range vals {
		out[k] = vals.Get(k)
	}
	return out, true
}

func isJSONBody(body []byte, contentType string) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json")) {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func logExchangeRequest(prov *oauthProvider, body []byte, contentType string) {
	vals, ok := requestFields(body, contentType)
	if !ok {
		log.Printf("oauth: %s exchange request is neither form- nor JSON-encoded; request fields not logged", prov.name)
		return
	}
	parts := []string{"grant_type=" + vals["grant_type"]}
	if c := vals["code"]; c != "" {
		parts = append(parts, fmt.Sprintf("code=%s(len %d)", oauthFingerprint(c), len(c)))
	}
	if v := vals["code_verifier"]; v != "" {
		parts = append(parts, fmt.Sprintf("code_verifier(len %d)", len(v)))
	}
	if r := vals["redirect_uri"]; r != "" {
		parts = append(parts, "redirect_uri="+r)
	}
	if id := vals["client_id"]; id != "" {
		parts = append(parts, "client_id="+id)
	}
	if rt := vals["refresh_token"]; rt != "" {
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

func (p *Proxy) redeemNonce(v string, prov *oauthProvider) (string, bool) {
	if !strings.Contains(v, "hsh_") {
		return v, false
	}
	t, found := p.store.lookup(v)
	if !found || t.Provider != prov.name {
		return v, false
	}
	log.Printf("oauth: redeemed %s %s nonce for token exchange", t.Provider, t.Field)
	return t.Value, true
}

func (p *Proxy) redeemRequestBody(body []byte, contentType string, prov *oauthProvider) []byte {
	if len(prov.nonceFields) == 0 || len(body) == 0 {
		return body
	}
	if isJSONBody(body, contentType) {
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return body
		}
		changed := false
		for _, f := range prov.nonceFields {
			if v, ok := doc[f].(string); ok {
				if real, ok := p.redeemNonce(v, prov); ok {
					doc[f] = real
					changed = true
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
			if real, ok := p.redeemNonce(v, prov); ok {
				vals[f][i] = real
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	return []byte(vals.Encode())
}

var tokenLikeFields = []string{"access_token", "refresh_token", "id_token", "accessToken", "refreshToken", "idToken"}

func successStatus(status int) bool {
	return status >= 200 && status < 300
}

func (p *Proxy) captureTokenResponse(body []byte, prov *oauthProvider, status int) ([]byte, error) {
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		if successStatus(status) {
			return nil, fmt.Errorf("%s token response is not JSON; refusing to pass it to the sandbox (check token_endpoint path and content type)", prov.name)
		}
		log.Printf("oauth: %s token error response is not JSON; passing it through", prov.name)
		return body, nil
	}
	changed := false
	for _, f := range prov.fields {
		real, ok := doc[f.Path].(string)
		if !ok || real == "" {
			continue
		}
		if _, known := p.store.lookup(real); known {
			continue
		}
		phantom, err := presentedPhantom(f, real)
		if err != nil {
			return nil, fmt.Errorf("generate a phantom for %s %s: %w", prov.name, f.Path, err)
		}
		p.store.put(phantom, storedToken{Provider: prov.name, Field: f.Path, Value: real})
		doc[f.Path] = phantom
		changed = true
		log.Printf("oauth: captured %s %s (sandbox sees a phantom)", prov.name, f.Path)
	}
	if !changed {
		if successStatus(status) {
			for _, k := range tokenLikeFields {
				if v, ok := doc[k].(string); ok && v != "" {
					if _, known := p.store.lookup(v); !known {
						return nil, fmt.Errorf("%s token response carries %s, which the descriptor does not capture (declared: %v); refusing to pass it to the sandbox", prov.name, k, prov.fieldNames())
					}
				}
			}
		}
		log.Printf("oauth: %s token response contained none of the declared fields %v; passing it through unchanged", prov.name, prov.fieldNames())
		return body, nil
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func encodingNote(ce string) string {
	if ce == "" {
		return ""
	}
	return ", decoded from " + ce
}

func decodeTokenResponse(resp *http.Response, body []byte) ([]byte, error) {
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch ce {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("token response: gzip: %w", err)
		}
		defer zr.Close()
		out, err := readLimited(zr, oauthBodyLimit)
		if err != nil {
			return nil, fmt.Errorf("token response: gzip: %w", err)
		}
		resp.Header.Del("Content-Encoding")
		return out, nil
	default:
		return nil, fmt.Errorf("token response uses unsupported Content-Encoding %q", ce)
	}
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
	ct := req.Header.Get("Content-Type")
	logExchangeRequest(prov, body, ct)
	body = p.redeemRequestBody(body, ct, prov)
	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	} else {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	removeHopHeaders(req.Header)
	req.Header.Set("Accept-Encoding", "gzip")
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
	encoding := resp.Header.Get("Content-Encoding")
	respBody, err = decodeTokenResponse(resp, respBody)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("oauth: %s token exchange: upstream %s (%s, %d bytes%s)", prov.name, resp.Status, resp.Header.Get("Content-Type"), len(respBody), encodingNote(encoding))
	if resp.StatusCode >= 400 {
		log.Printf("oauth: token endpoint refused the exchange: %s", oauthErrorSummary(respBody))
	}
	respBody, err = p.captureTokenResponse(respBody, prov, resp.StatusCode)
	if err != nil {
		return nil, nil, err
	}
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
