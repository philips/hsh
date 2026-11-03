package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
)

type loginUpstream struct {
	srv       *httptest.Server
	lastAuth  atomic.Value
	tokenHits atomic.Int32
	apiStatus atomic.Int32
}

func newLoginUpstream(t *testing.T) *loginUpstream {
	t.Helper()
	u := &loginUpstream{}
	u.apiStatus.Store(200)
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			u.tokenHits.Add(1)
			http.Error(w, "the host refreshes; the sandbox's refresh must be answered by the proxy", http.StatusTeapot)
			return
		}
		u.lastAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(int(u.apiStatus.Load()))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func marker(c string) string {
	return "hsh_" + strings.Repeat(c, 64)
}

func loginSetup(t *testing.T, failure string, expiresIn time.Duration) (*loginUpstream, string, config.ResolvedOAuthSecret, oauthprov.Resolved) {
	t.Helper()
	dir := t.TempDir()
	up := newLoginUpstream(t)
	host := upstreamHost(up.srv.URL)
	prov := oauthprov.Resolved{
		Name:               "anthropiclike",
		TokenEndpoint:      oauthprov.Endpoint{Host: host, Path: "/token"},
		ResponseFields:     []oauthprov.Field{{Path: "access_token", Format: "sk-ant-oat01-{phantom}"}, {Path: "refresh_token", Format: "sk-ant-ort01-{phantom}"}},
		RequestNonceFields: []string{"refresh_token"},
		APIHosts:           []string{host},
		Header:             "Authorization",
		Prefix:             "Bearer ",
	}
	login := config.ResolvedOAuthSecret{
		Name:        "claude-login",
		Provider:    "anthropiclike",
		AccessToken: "real-access-current",
		ExpiresAt:   time.Now().Add(expiresIn).UTC().Format(time.RFC3339),
		Failure:     failure,
		Markers:     map[string]string{marker("a"): "access_token", marker("b"): "refresh_token", marker("c"): "refresh_token"},
		Presented:   map[string]string{"access_token": "sk-ant-oat01-" + marker("a"), "refresh_token": "sk-ant-ort01-" + marker("b")},
		Meta:        map[string]string{"scope": "user:inference", "token_type": "Bearer"},
		Hosts:       []string{host},
	}
	captured := map[string]storedToken{
		"sk-ant-ort01-" + marker("c"): {Provider: "anthropiclike", Field: "refresh_token", Value: "real-refresh-captured"},
		"sk-ant-oat01-" + marker("d"): {Provider: "anthropiclike", Field: "access_token", Value: "real-unrelated"},
	}
	data, _ := json.Marshal(captured)
	if err := os.WriteFile(filepath.Join(dir, "oauth-tokens.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, _ := json.Marshal(config.ResolvedSecrets{Secrets: []config.ResolvedSecret{}, OAuth: []oauthprov.Resolved{prov}, OAuthSecrets: []config.ResolvedOAuthSecret{login}})
	addr, _ := startTestProxyMode(t, dir, writeConfig(t, dir, up.srv.URL), nil, "", string(secrets))
	return up, addr, login, prov
}

func doAPI(t *testing.T, client *http.Client, target, auth string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("Authorization", auth)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestLoginRedeemsByMarker(t *testing.T) {
	up, addr, _, _ := loginSetup(t, "", time.Hour)
	client := proxyClient(addr)
	if code, _ := doAPI(t, client, up.srv.URL+"/v1/messages", "Bearer sk-ant-oat01-"+marker("a")); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := up.lastAuth.Load(); got != "Bearer real-access-current" {
		t.Fatalf("upstream saw %v", got)
	}
	if code, _ := doAPI(t, client, up.srv.URL+"/v1/messages", "Bearer eyJold.eyJstaleclaims."+marker("a")); code != 200 || up.lastAuth.Load() != "Bearer real-access-current" {
		t.Fatalf("a JWT phantom with stale claims must still redeem by its marker; upstream saw %v", up.lastAuth.Load())
	}
	doAPI(t, client, up.srv.URL+"/v1/messages", "Bearer sk-ant-ort01-"+marker("b"))
	if got := up.lastAuth.Load(); got != "Bearer sk-ant-ort01-"+marker("b") {
		t.Fatalf("a refresh phantom must never redeem as a bearer token; upstream saw %v", got)
	}
}

func TestLoginLocalRefreshAnswer(t *testing.T) {
	up, addr, login, _ := loginSetup(t, "", time.Hour)
	client := proxyClient(addr)
	body := fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"sk-ant-ort01-%s","client_id":"c"}`, marker("c"))
	code, doc := postToken(t, client, up.srv.URL+"/token", "application/json", body)
	if code != 200 {
		t.Fatalf("local answer status %d: %v", code, doc)
	}
	if up.tokenHits.Load() != 0 {
		t.Fatal("a refresh carrying a login's refresh phantom must never reach the token endpoint")
	}
	if doc["access_token"] != login.Presented["access_token"] || doc["refresh_token"] != login.Presented["refresh_token"] || doc["scope"] != "user:inference" {
		t.Fatalf("local answer = %v", doc)
	}
	exp, _ := doc["expires_in"].(float64)
	if exp < 3500 || exp > 3600 {
		t.Fatalf("expires_in must mirror the real expiry, got %v", doc["expires_in"])
	}
	if strings.Contains(fmt.Sprint(doc), "real-") {
		t.Fatalf("the local answer leaked a real token: %v", doc)
	}
	code, doc = postToken(t, client, up.srv.URL+"/token", "application/json", body)
	if code != 503 {
		t.Fatalf("a second refresh within the interval must be rate-limited, got %d %v", code, doc)
	}
	if doc["error"] == "invalid_grant" {
		t.Fatal("never answer invalid_grant: Claude Code wipes its credentials on it")
	}
}

func TestLoginLocalAnswerNearExpiryAsksHost(t *testing.T) {
	up, addr, _, _ := loginSetup(t, "", 5*time.Minute)
	client := proxyClient(addr)
	body := fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"sk-ant-ort01-%s"}`, marker("b"))
	if code, _ := postToken(t, client, up.srv.URL+"/token", "application/json", body); code != 200 {
		t.Fatalf("status %d", code)
	}
	if _, err := os.Stat(filepath.Join(proxyDirOf(t, addr), "refresh-wanted", "claude-login")); err != nil {
		t.Fatalf("a refresh near expiry must ask the host to refresh: %v", err)
	}
}

func TestLoginUpstream401AsksHost(t *testing.T) {
	up, addr, _, _ := loginSetup(t, "", time.Hour)
	up.apiStatus.Store(401)
	doAPI(t, proxyClient(addr), up.srv.URL+"/v1/messages", "Bearer sk-ant-oat01-"+marker("a"))
	if _, err := os.Stat(filepath.Join(proxyDirOf(t, addr), "refresh-wanted", "claude-login")); err != nil {
		t.Fatalf("a 401 on a redeemed login must ask the host to refresh: %v", err)
	}
}

func TestLoginFailureAnswers401(t *testing.T) {
	up, addr, _, _ := loginSetup(t, "invalid_grant: revoked", time.Hour)
	code, body := doAPI(t, proxyClient(addr), up.srv.URL+"/v1/messages", "Bearer sk-ant-oat01-"+marker("a"))
	if code != 401 || !strings.Contains(body, "hsh: the OAuth login claude-login stopped working") {
		t.Fatalf("status %d body %s", code, body)
	}
	if up.lastAuth.Load() != nil {
		t.Fatal("a failed login must not be forwarded")
	}
}

func TestLoginPrunesBoundCaptures(t *testing.T) {
	_, addr, _, _ := loginSetup(t, "", time.Hour)
	data, err := os.ReadFile(filepath.Join(proxyDirOf(t, addr), "oauth-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "real-refresh-captured") {
		t.Fatalf("the captured refresh token bound to a login must be dropped: %s", data)
	}
	if !strings.Contains(string(data), "real-unrelated") {
		t.Fatalf("unbound captures must stay: %s", data)
	}
}

var proxyDirs = map[string]string{}

func proxyDirOf(t *testing.T, addr string) string {
	t.Helper()
	dir, ok := proxyDirs[addr]
	if !ok {
		t.Fatalf("no test dir recorded for %s", addr)
	}
	return dir
}
