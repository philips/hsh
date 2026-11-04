package proxy

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func tokenHostUpstream(t *testing.T, tlsUpstream bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"real-access-ok","refresh_token":"real-refresh-ok"}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"real-access-leak","refresh_token":"real-refresh-leak"}`)
	})
	mux.HandleFunc("GET /gz", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			fmt.Fprint(w, `{"id_token":"real-id-leak"}`)
			return
		}
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		fmt.Fprint(zw, `{"id_token":"real-id-leak"}`)
		zw.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(b.Bytes())
	})
	mux.HandleFunc("GET /br", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		fmt.Fprint(w, "opaque")
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"issuer":"https://auth.example","token_endpoint":"/token"}`)
	})
	mux.HandleFunc("GET /refused", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid","access_token":"not-a-token"}`)
	})
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(bytes.Repeat([]byte("x"), oauthBodyLimit+10))
	})
	var srv *httptest.Server
	if tlsUpstream {
		srv = httptest.NewTLSServer(mux)
	} else {
		srv = httptest.NewServer(mux)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestTokenBearingResponsesOutsideTheTokenEndpointAreBlocked(t *testing.T) {
	for _, tlsUpstream := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%v", tlsUpstream), func(t *testing.T) {
			dir := t.TempDir()
			srv := tokenHostUpstream(t, tlsUpstream)
			configPath := writeConfig(t, dir, srv.URL)
			var rootsPEM []byte
			if tlsUpstream {
				rootsPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
			}
			addr, caPool := startTestProxy(t, dir, configPath, rootsPEM, testProvider(upstreamHost(srv.URL)))
			client := &http.Client{Transport: &http.Transport{
				Proxy:              func(*http.Request) (*url.URL, error) { return url.Parse("http://" + addr) },
				TLSClientConfig:    &tls.Config{RootCAs: caPool},
				DisableCompression: true,
			}}
			do := func(method, path string, header map[string]string) (int, string) {
				t.Helper()
				req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("grant_type=password"))
				if method == http.MethodPost {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				for k, v := range header {
					req.Header.Set(k, v)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(body)
			}
			if code, body := do(http.MethodPost, "/token", nil); code != 200 || strings.Contains(body, "real-access-ok") {
				t.Fatalf("the token endpoint: %d %s", code, body)
			}
			if code, body := do(http.MethodPost, "/oauth/token", nil); code != http.StatusBadGateway || strings.Contains(body, "real-") {
				t.Fatalf("a token response on another path passed: %d %s", code, body)
			}
			if code, body := do(http.MethodGet, "/gz", map[string]string{"Accept-Encoding": "gzip, br, zstd"}); code != http.StatusBadGateway || strings.Contains(body, "real-") {
				t.Fatalf("a gzipped token response passed: %d %q", code, body)
			}
			if code, body := do(http.MethodGet, "/gz", nil); code != http.StatusBadGateway || strings.Contains(body, "real-") {
				t.Fatalf("a token response without Accept-Encoding passed: %d %q", code, body)
			}
			if code, _ := do(http.MethodGet, "/br", map[string]string{"Accept-Encoding": "br"}); code == http.StatusOK {
				t.Fatal("a response in an encoding hsh cannot check passed")
			}
			if code, body := do(http.MethodGet, "/config", nil); code != 200 || !strings.Contains(body, "issuer") {
				t.Fatalf("an ordinary JSON response was blocked: %d %s", code, body)
			}
			if code, body := do(http.MethodGet, "/refused", nil); code != http.StatusUnauthorized || !strings.Contains(body, "invalid") {
				t.Fatalf("an error response was changed: %d %s", code, body)
			}
			if code, body := do(http.MethodGet, "/big", nil); code != 200 || len(body) != oauthBodyLimit+10 {
				t.Fatalf("a large response was not streamed through: %d %d", code, len(body))
			}
		})
	}
}
