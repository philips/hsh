package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
)

const (
	localAnswerInterval = 30 * time.Second
	refreshWantedWithin = 15 * time.Minute
)

type markerRef struct {
	secret int
	field  string
}

type redeemResult struct {
	ok      bool
	secret  string
	failure string
}

func buildMarkerIndex(secrets []config.ResolvedOAuthSecret) map[string]markerRef {
	idx := map[string]markerRef{}
	for i, s := range secrets {
		for marker, field := range s.Markers {
			idx[marker] = markerRef{secret: i, field: field}
		}
	}
	return idx
}

func (p *Proxy) providerByName(name string) (oauthProvider, bool) {
	for _, prov := range p.st.oauth {
		if prov.name == name {
			return prov, true
		}
	}
	return oauthProvider{}, false
}

func (p *Proxy) redeemOAuthSecret(h http.Header, host string) redeemResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, s := range p.st.oauthSecrets {
		if !HostMatches(s.Hosts, host) {
			continue
		}
		prov, ok := p.providerByName(s.Provider)
		if !ok {
			continue
		}
		value := h.Get(prov.header)
		if len(value) < len(prov.prefix) || value[:len(prov.prefix)] != prov.prefix {
			continue
		}
		marker := oauthprov.MarkerPattern.FindString(value[len(prov.prefix):])
		ref, ok := p.st.markers[marker]
		if marker == "" || !ok || p.st.oauthSecrets[ref.secret].Name != s.Name || ref.field != "access_token" {
			continue
		}
		if s.Failure != "" {
			return redeemResult{secret: s.Name, failure: s.Failure}
		}
		h.Set(prov.header, prov.prefix+s.AccessToken)
		log.Printf("oauth: redeemed login %s (%s) for %s", s.Name, s.Provider, host)
		return redeemResult{ok: true, secret: s.Name}
	}
	return redeemResult{}
}

func loginFailureBody(name, failure string) []byte {
	msg := fmt.Sprintf("hsh: the OAuth login %s stopped working (%s). Log in again inside a sandbox and run: hsh oauth promote --force PROVIDER %s", name, failure, name)
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "authentication_error", "message": msg}})
	return body
}

func (p *Proxy) writeLoginFailureHTTP(w http.ResponseWriter, r redeemResult) {
	log.Printf("oauth: login %s has failed (%s); answering 401", r.secret, r.failure)
	body := loginFailureBody(r.secret, r.failure)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write(body)
}

func (p *Proxy) writeLoginFailureConn(w io.Writer, r redeemResult) {
	log.Printf("oauth: login %s has failed (%s); answering 401", r.secret, r.failure)
	writeJSONResponse(w, http.StatusUnauthorized, nil, loginFailureBody(r.secret, r.failure))
}

func writeJSONResponse(w io.Writer, status int, extra http.Header, body []byte) {
	resp := &http.Response{
		StatusCode:    status,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	for k, vv := range extra {
		resp.Header[k] = vv
	}
	_ = resp.Write(w)
}

func (p *Proxy) refreshWanted(secret, reason string) {
	if p.opts.OAuthStore == "" {
		return
	}
	dir := filepath.Join(filepath.Dir(p.opts.OAuthStore), "refresh-wanted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("oauth: refresh-wanted marker: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, url.PathEscape(secret)), []byte(reason+"\n"), 0o600); err != nil {
		log.Printf("oauth: refresh-wanted marker: %v", err)
		return
	}
	log.Printf("oauth: asked the host to refresh login %s (%s)", secret, reason)
}

func (p *Proxy) noteStatus(r redeemResult) func(int) {
	if !r.ok {
		return nil
	}
	return func(status int) {
		if status == http.StatusUnauthorized {
			p.refreshWanted(r.secret, "upstream answered 401")
		}
	}
}

func (p *Proxy) localRefreshTarget(body []byte, contentType string, prov *oauthProvider) (config.ResolvedOAuthSecret, bool) {
	vals, ok := requestFields(body, contentType)
	if !ok {
		return config.ResolvedOAuthSecret{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, f := range prov.nonceFields {
		marker := oauthprov.MarkerPattern.FindString(vals[f])
		if marker == "" {
			continue
		}
		ref, ok := p.st.markers[marker]
		if !ok {
			continue
		}
		s := p.st.oauthSecrets[ref.secret]
		if s.Provider == prov.name && ref.field == "refresh_token" {
			return s, true
		}
	}
	return config.ResolvedOAuthSecret{}, false
}

func (p *Proxy) localRefreshAnswer(s config.ResolvedOAuthSecret, prov *oauthProvider) (*http.Response, []byte) {
	now := time.Now()
	p.localMu.Lock()
	last := p.lastLocal[s.Name]
	limited := !last.IsZero() && now.Sub(last) < localAnswerInterval
	if !limited {
		p.lastLocal[s.Name] = now
	}
	p.localMu.Unlock()
	if limited {
		log.Printf("oauth: login %s: refresh asked again within %s; answering 503", s.Name, localAnswerInterval)
		body, _ := json.Marshal(map[string]string{"error": "temporarily_unavailable", "error_description": "hsh: refresh requested too often; retry later"})
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Header: http.Header{"Content-Type": {"application/json"}, "Retry-After": {strconv.Itoa(int(localAnswerInterval.Seconds()))}}}, body
	}
	doc := map[string]any{}
	for k, v := range s.Meta {
		doc[k] = v
	}
	for _, f := range prov.fields {
		if v, ok := s.Presented[f.Path]; ok {
			doc[f.Path] = v
		}
	}
	expiresAt, _ := time.Parse(time.RFC3339, s.ExpiresAt)
	remaining := time.Until(expiresAt)
	if remaining < 0 {
		remaining = 0
	}
	doc[oauthprov.ExpiresField(prov.expiresIn)] = int(remaining.Seconds())
	if remaining < refreshWantedWithin {
		p.refreshWanted(s.Name, "the client refreshed and the token expires in "+remaining.Round(time.Second).String())
	}
	body, _ := json.Marshal(doc)
	log.Printf("oauth: answered a %s refresh locally for login %s (expires in %s)", prov.name, s.Name, remaining.Round(time.Second))
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}}, body
}

func (p *Proxy) pruneBoundCaptures() {
	p.mu.RLock()
	markers := p.st.markers
	p.mu.RUnlock()
	if len(markers) == 0 {
		return
	}
	n := p.store.prune(func(key string) bool {
		_, bound := markers[oauthprov.MarkerPattern.FindString(key)]
		return bound
	})
	if n > 0 {
		log.Printf("oauth: dropped %d captured token(s) now bound to a host login", n)
	}
}
