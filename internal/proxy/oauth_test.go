package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/certs"
)

type oauthUpstream struct {
	srv       *httptest.Server
	access    string
	refresh   string
	refreshes []string
}

func newOAuthUpstream(t *testing.T, tlsUpstream bool) *oauthUpstream {
	t.Helper()
	u := &oauthUpstream{access: "real-access-1", refresh: "real-refresh-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "password":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":%q,"expires_in":3600}`, u.access, u.refresh)
		case "refresh_token":
			got := r.PostForm.Get("refresh_token")
			u.refreshes = append(u.refreshes, got)
			if got != u.refresh {
				http.Error(w, "invalid refresh token", http.StatusUnauthorized)
				return
			}
			u.refresh = "real-refresh-2"
			u.access = "real-access-2"
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":%q,"expires_in":3600}`, u.access, u.refresh)
		default:
			http.Error(w, "unsupported grant", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+u.access {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "api-ok")
	})
	if tlsUpstream {
		u.srv = httptest.NewTLSServer(mux)
	} else {
		u.srv = httptest.NewServer(mux)
	}
	t.Cleanup(u.srv.Close)
	return u
}

func writeConfig(t *testing.T, dir string, upstreamURL string) (configPath string) {
	t.Helper()
	host := upstreamHost(upstreamURL)
	cfg := fmt.Sprintf(`{
  "network": {"allow": [%q]},
  "oauth": {"testprov": {
    "token_endpoint": {"host": %q, "path": "/token"},
    "response_fields": ["access_token", "refresh_token"],
    "request_nonce_fields": ["refresh_token"],
    "api_hosts": [%q]
  }}
}`, host, host, host)
	configPath = filepath.Join(dir, "config.hsh")
	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func upstreamHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u.Hostname()
}

func startTestProxy(t *testing.T, dir, configPath string, rootsPEM []byte) (addr string, caPool *x509.CertPool) {
	t.Helper()
	ca, err := certs.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), ca.KeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.json"), []byte(`{"secrets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "oauth-tokens.json")
	opts := Options{
		ListenAddr:  "127.0.0.1:0",
		ConfigPath:  configPath,
		SecretsPath: filepath.Join(dir, "secrets.json"),
		CACertPath:  filepath.Join(dir, "ca.crt"),
		CAKeyPath:   filepath.Join(dir, "ca.key"),
		DeniedDB:    "",
		OAuthStore:  storePath,
	}
	if rootsPEM != nil {
		rootsPath := filepath.Join(dir, "roots.pem")
		if err := os.WriteFile(rootsPath, rootsPEM, 0o644); err != nil {
			t.Fatal(err)
		}
		opts.RootsPath = rootsPath
	}
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.reload(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = (&http.Server{Handler: p}).Serve(ln) }()
	t.Cleanup(func() { ln.Close() })
	caPool = x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("parse proxy CA")
	}
	return ln.Addr().String(), caPool
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func isPhantom(s string) bool {
	return strings.HasPrefix(s, "hsh_") && len(s) == len("hsh_")+64
}

func testExchange(t *testing.T, client *http.Client, upstreamURL string) (access, refresh string) {
	t.Helper()
	resp, err := client.Post(upstreamURL+"/token", "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=password&username=u&password=p"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatal(err)
	}
	if !isPhantom(tr.AccessToken) || !isPhantom(tr.RefreshToken) {
		t.Fatalf("sandbox did not receive phantoms: %+v", tr)
	}
	if strings.Contains(tr.AccessToken, "real") {
		t.Fatal("real access token leaked to the sandbox")
	}
	return tr.AccessToken, tr.RefreshToken
}

func testAPIWithPhantom(t *testing.T, client *http.Client, upstreamURL, phantom string) {
	t.Helper()
	req, err := http.NewRequest("GET", upstreamURL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+phantom)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "api-ok" {
		t.Fatalf("api call with phantom failed: %d %s", resp.StatusCode, body)
	}
}

func testRefresh(t *testing.T, client *http.Client, upstreamURL, refreshPhantom string) {
	t.Helper()
	resp, err := client.Post(upstreamURL+"/token", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshPhantom}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatal(err)
	}
	if !isPhantom(tr.AccessToken) || !isPhantom(tr.RefreshToken) {
		t.Fatalf("refresh response did not contain phantoms: %+v", tr)
	}
	if strings.Contains(tr.AccessToken, "real") || strings.Contains(tr.RefreshToken, "real") {
		t.Fatal("real tokens leaked in refresh response")
	}
}

func testStore(t *testing.T, storePath string) {
	t.Helper()
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal("oauth store was not written:", err)
	}
	var m map[string]storedToken
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var access, refresh int
	for _, v := range m {
		switch v.Field {
		case "access_token":
			access++
		case "refresh_token":
			refresh++
		}
		if !strings.HasPrefix(v.Value, "real-") {
			t.Fatalf("store entry does not hold the real token: %+v", v)
		}
	}
	if access == 0 || refresh == 0 {
		t.Fatalf("store missing captured fields: %s", data)
	}
}

func TestOAuthCapturePlainHTTP(t *testing.T) {
	dir := t.TempDir()
	up := newOAuthUpstream(t, false)
	configPath := writeConfig(t, dir, up.srv.URL)
	addr, _ := startTestProxy(t, dir, configPath, nil)
	client := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		return url.Parse("http://" + addr)
	}}}
	access, refresh := testExchange(t, client, up.srv.URL)
	testAPIWithPhantom(t, client, up.srv.URL, access)
	testRefresh(t, client, up.srv.URL, refresh)
	if len(up.refreshes) != 1 || up.refreshes[0] != "real-refresh-1" {
		t.Fatalf("upstream did not receive the real refresh token: %v", up.refreshes)
	}
	testStore(t, filepath.Join(dir, "oauth-tokens.json"))
}

func TestOAuthCaptureTLS(t *testing.T) {
	dir := t.TempDir()
	up := newOAuthUpstream(t, true)
	configPath := writeConfig(t, dir, up.srv.URL)
	cert := up.srv.Certificate()
	if cert == nil {
		t.Fatal("no upstream certificate")
	}
	rootsPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	addr, caPool := startTestProxy(t, dir, configPath, rootsPEM)
	client := &http.Client{Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://" + addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: caPool},
	}}
	access, refresh := testExchange(t, client, up.srv.URL)
	testAPIWithPhantom(t, client, up.srv.URL, access)
	testRefresh(t, client, up.srv.URL, refresh)
	if len(up.refreshes) != 1 || up.refreshes[0] != "real-refresh-1" {
		t.Fatalf("upstream did not receive the real refresh token: %v", up.refreshes)
	}
	testStore(t, filepath.Join(dir, "oauth-tokens.json"))
}
