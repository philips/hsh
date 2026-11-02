package proxy

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hsh/internal/oauthprov"
)

type encodingUpstream struct {
	srv       *httptest.Server
	body      string
	status    int
	encoding  string
	gotAccept string
}

func newEncodingUpstream(t *testing.T) *encodingUpstream {
	t.Helper()
	u := &encodingUpstream{status: 200, body: `{"access_token":"sk-ant-oat01-real-1","refresh_token":"sk-ant-ort01-real-1","expires_in":3600}`}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.gotAccept = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case u.encoding == "br":
			w.Header().Set("Content-Encoding", "br")
			w.WriteHeader(u.status)
			fmt.Fprint(w, "\x0b\x02\x80not-really-brotli")
		case strings.Contains(r.Header.Get("Accept-Encoding"), "gzip"):
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(u.status)
			zw := gzip.NewWriter(w)
			fmt.Fprint(zw, u.body)
			zw.Close()
		default:
			w.WriteHeader(u.status)
			fmt.Fprint(w, u.body)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func encodingProvider(host string) oauthprov.Resolved {
	return oauthprov.Resolved{
		Name:          "anthropiclike",
		TokenEndpoint: oauthprov.Endpoint{Host: host, Path: "/v1/oauth/token"},
		ResponseFields: []oauthprov.Field{
			{Path: "access_token", Format: "sk-ant-oat01-{phantom}"},
			{Path: "refresh_token", Format: "sk-ant-ort01-{phantom}"},
		},
		APIHosts: []string{host},
		Header:   "Authorization",
		Prefix:   "Bearer ",
	}
}

func clientLikeClaude(t *testing.T, client *http.Client, target string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", target, strings.NewReader(`{"grant_type":"authorization_code","code":"c"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestOAuthCapturesCompressedTokenResponse(t *testing.T) {
	dir := t.TempDir()
	up := newEncodingUpstream(t)
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil, encodingProvider(upstreamHost(up.srv.URL)))
	status, hdr, body := clientLikeClaude(t, proxyClient(addr), up.srv.URL+"/v1/oauth/token")
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	if up.gotAccept != "gzip" {
		t.Fatalf("the proxy must ask the token endpoint for an encoding it can decode, upstream saw %q", up.gotAccept)
	}
	if hdr.Get("Content-Encoding") != "" {
		t.Fatalf("the rewritten body is plain, but Content-Encoding is %q", hdr.Get("Content-Encoding"))
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the client got a body it cannot parse: %v: %q", err, body)
	}
	if strings.Contains(body, "real-") {
		t.Fatalf("real tokens reached the client: %s", body)
	}
	if a, _ := doc["access_token"].(string); !strings.HasPrefix(a, "sk-ant-oat01-hsh_") {
		t.Fatalf("access_token = %q", a)
	}
}

func TestOAuthFailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		encoding   string
		wantStatus int
	}{
		{name: "unsupported encoding", encoding: "br", status: 200, wantStatus: 502},
		{name: "not JSON", body: "access_token=real-secret", status: 200, wantStatus: 502},
		{name: "undeclared token field", body: `{"accessToken":"real-secret"}`, status: 200, wantStatus: 502},
		{name: "undeclared token field next to a captured one", body: `{"access_token":"sk-ant-oat01-real-1","id_token":"real-secret"}`, status: 200, wantStatus: 502},
		{name: "refresh without a rotated refresh token", body: `{"access_token":"sk-ant-oat01-real-2","expires_in":3600}`, status: 200, wantStatus: 200},
		{name: "error response passes", body: "invalid_grant: real-looking but not a token", status: 400, wantStatus: 400},
		{name: "no tokens passes", body: `{"ok":true}`, status: 200, wantStatus: 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			up := newEncodingUpstream(t)
			if c.body != "" {
				up.body = c.body
			}
			up.status, up.encoding = c.status, c.encoding
			addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil, encodingProvider(upstreamHost(up.srv.URL)))
			status, _, body := clientLikeClaude(t, proxyClient(addr), up.srv.URL+"/v1/oauth/token")
			if status != c.wantStatus {
				t.Fatalf("status %d, want %d (body %q)", status, c.wantStatus, body)
			}
			if c.wantStatus == 502 && strings.Contains(body, "real-") {
				t.Fatalf("a blocked response leaked its body: %q", body)
			}
		})
	}
}
