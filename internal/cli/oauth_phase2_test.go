package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
	"hsh/internal/secretstore"
	"hsh/internal/workspace"
)

type fakeTokenEndpoint struct {
	srv       *httptest.Server
	mu        sync.Mutex
	refreshes []string
	accept    map[string]bool
	gen       int
}

func phase2JWT(gen int) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256"}) + "." + enc(map[string]any{"gen": gen, "exp": time.Now().Add(time.Hour).Unix()}) + fmt.Sprintf(".real-sig-%d", gen)
}

func newFakeTokenEndpoint(t *testing.T, accept ...string) *fakeTokenEndpoint {
	t.Helper()
	f := &fakeTokenEndpoint{accept: map[string]bool{}}
	for _, a := range accept {
		f.accept[a] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.refreshes = append(f.refreshes, req["refresh_token"])
		w.Header().Set("Content-Type", "application/json")
		if !f.accept[req["refresh_token"]] {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		f.gen++
		delete(f.accept, req["refresh_token"])
		next := fmt.Sprintf("real-refresh-%d", f.gen+100)
		f.accept[next] = true
		fmt.Fprintf(w, `{"access_token":%q,"id_token":%q,"refresh_token":%q,"expires_in":3600,"scope":"user:inference"}`, phase2JWT(f.gen), phase2JWT(f.gen), next)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeTestProvider(t *testing.T, refreshURL string) {
	t.Helper()
	dir := oauthProvidersDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	desc := map[string]any{
		"version":              1,
		"token_endpoint":       map[string]string{"host": "auth.example", "path": "/token"},
		"response_fields":      []any{map[string]string{"path": "access_token", "kind": "jwt"}, map[string]string{"path": "id_token", "kind": "jwt"}, map[string]string{"path": "refresh_token", "format": "rt-{phantom}"}},
		"request_nonce_fields": []string{"refresh_token"},
		"api":                  map[string]any{"hosts": []string{"api.example"}},
		"refresh":              map[string]string{"client_id": "cid", "request_body": "json", "url": refreshURL},
		"seeds": map[string]any{"creds": map[string]string{
			"path":     "/home/hsh/.example/creds.json",
			"template": `{"access":{{json .access_token}},"refresh":{{json .refresh_token}},"id":{{json .id_token}},"expires":{{.expires_at_millis}},"scope":{{json .meta.scope}}}`,
		}},
	}
	data, _ := json.Marshal(desc)
	if err := os.WriteFile(filepath.Join(dir, "testlogin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type phase2Env struct {
	sessionDir string
	storePath  string
	store      *secretstore.DB
	endpoint   *fakeTokenEndpoint
}

func setupPhase2(t *testing.T, configJSON string, accept ...string) *phase2Env {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ep := newFakeTokenEndpoint(t, accept...)
	writeTestProvider(t, ep.srv.URL)
	storePath := filepath.Join(t.TempDir(), "secrets.db")
	store, _, err := secretstore.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sessionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return &phase2Env{sessionDir: sessionDir, storePath: storePath, store: store, endpoint: ep}
}

func readSecretsJSON(t *testing.T, sessionDir string) (config.ResolvedSecrets, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rs config.ResolvedSecrets
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatal(err)
	}
	return rs, string(data)
}

func TestResolveSessionOAuthStablePhantomsAndSeeds(t *testing.T) {
	env := setupPhase2(t, `{"version": 1, "secrets": {"LOGIN": {"from": "hsh://work", "seed": ["creds"]}}}`)
	login := config.OAuthSecret{Name: "work", Provider: "testlogin", Tokens: map[string]string{"access_token": phase2JWT(1), "id_token": phase2JWT(1), "refresh_token": "real-refresh-1"}, Meta: map[string]string{"scope": "user:inference"}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := env.store.PutOAuth(login, false); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(env.sessionDir, "config.json"))
	res, err := resolveSessionSecrets(env.sessionDir, cfg, env.store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets.OAuthSecrets) != 1 || len(res.Secrets.OAuth) != 1 || res.Secrets.OAuth[0].Name != "testlogin" {
		t.Fatalf("resolved = %+v", res.Secrets)
	}
	first := res.Secrets.OAuthSecrets[0]
	data, _ := json.Marshal(res.Secrets)
	if strings.Contains(string(data), "real-refresh") {
		t.Fatal("secrets.json must never carry the refresh token")
	}
	if first.AccessToken != login.Tokens["access_token"] || !strings.HasPrefix(first.Presented["refresh_token"], "rt-hsh_") {
		t.Fatalf("first = %+v", first)
	}
	accessMarker := oauthprov.MarkerPattern.FindString(first.Presented["access_token"])
	login.Tokens = map[string]string{"access_token": phase2JWT(2), "id_token": phase2JWT(2), "refresh_token": "real-refresh-2"}
	if err := env.store.PutOAuth(login, true); err != nil {
		t.Fatal(err)
	}
	res, err = resolveSessionSecrets(env.sessionDir, cfg, env.store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second := res.Secrets.OAuthSecrets[0]
	if oauthprov.MarkerPattern.FindString(second.Presented["access_token"]) != accessMarker {
		t.Fatal("the session's phantom marker must survive a refresh")
	}
	if second.Presented["access_token"] == first.Presented["access_token"] {
		t.Fatal("a JWT phantom must carry the refreshed token's claims")
	}
	manifest, err := os.ReadFile(filepath.Join(sessionSeedDir(env.sessionDir), workspace.SeedManifest))
	if err != nil {
		t.Fatal(err)
	}
	var entries []workspace.SeedEntry
	_ = json.Unmarshal(manifest, &entries)
	if len(entries) != 1 || entries[0].Path != "/home/hsh/.example/creds.json" || entries[0].Mode != 0o600 {
		t.Fatalf("manifest = %s", manifest)
	}
	seed, _ := os.ReadFile(filepath.Join(sessionSeedDir(env.sessionDir), entries[0].File))
	var doc map[string]any
	if err := json.Unmarshal(seed, &doc); err != nil {
		t.Fatalf("seed is not JSON: %s", seed)
	}
	if doc["access"] != second.Presented["access_token"] || doc["scope"] != "user:inference" || strings.Contains(string(seed), "real-") {
		t.Fatalf("seed = %s", seed)
	}
}

func writeCapture(t *testing.T, sessionDir string, entries map[string]capturedToken) {
	t.Helper()
	dir := filepath.Join(sessionDir, "proxy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "oauth-tokens.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func capturedLogin(gen int, rt string, created time.Time) map[string]capturedToken {
	m := map[string]capturedToken{}
	jwt := phase2JWT(gen)
	hp, _ := oauthprov.JWTHeaderPayload(jwt)
	ts := created.UTC().Format(time.RFC3339Nano)
	m[hp+"."+marker(fmt.Sprint(gen))] = capturedToken{Provider: "testlogin", Field: "access_token", Value: jwt, Created: ts, ExpiresAt: created.Add(time.Hour).UTC().Format(time.RFC3339), Meta: map[string]string{"scope": "user:inference"}}
	m[hp+"."+marker("e")] = capturedToken{Provider: "testlogin", Field: "id_token", Value: jwt, Created: ts}
	m["rt-"+marker(string(rune('a'+gen)))] = capturedToken{Provider: "testlogin", Field: "refresh_token", Value: rt, Created: ts}
	m["other-"+marker("a")] = capturedToken{Provider: "someone-else", Field: "access_token", Value: "x", Created: ts}
	return m
}

func marker(c string) string {
	return "hsh_" + strings.Repeat(c, 64)
}

func TestPromoteLogin(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`, "real-refresh-1")
	writeCapture(t, env.sessionDir, capturedLogin(1, "real-refresh-1", time.Now()))
	if err := os.WriteFile(filepath.Join(env.sessionDir, "secrets.json"), []byte(`{"secrets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, provider: "testlogin", name: "work", store: env.store, client: env.endpoint.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hsh://work") || !strings.Contains(out, `"seed": ["creds"]`) {
		t.Fatalf("output:\n%s", out)
	}
	if len(env.endpoint.refreshes) != 1 || env.endpoint.refreshes[0] != "real-refresh-1" {
		t.Fatalf("verification refresh used %v", env.endpoint.refreshes)
	}
	login, err := env.store.GetOAuth("work")
	if err != nil {
		t.Fatal(err)
	}
	if login.Tokens["refresh_token"] != "real-refresh-101" || login.Failure != "" || login.Meta["scope"] != "user:inference" {
		t.Fatalf("stored login = %+v", login)
	}
	sp, _ := loadSessionPhantoms(env.sessionDir)
	if len(sp.Bound) != 3 {
		t.Fatalf("the session's captured phantoms must be bound (3 for this provider), got %v", sp.Bound)
	}
	rs, raw := readSecretsJSON(t, env.sessionDir)
	if len(rs.OAuthSecrets) != 1 || rs.OAuthSecrets[0].Name != "work" {
		t.Fatalf("the promoting session must pick up the login without a config entry: %s", raw)
	}
	for m := range sp.Bound {
		if _, ok := rs.OAuthSecrets[0].Markers[m]; !ok {
			t.Fatalf("bound marker %s missing from secrets.json", m)
		}
	}
	if strings.Contains(raw, "real-refresh") {
		t.Fatal("secrets.json leaked the refresh token")
	}
	if _, err := promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, provider: "testlogin", name: "work", store: env.store, client: env.endpoint.srv.Client()}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("promoting over an existing login needs --force: %v", err)
	}
}

func TestPromoteRetriesWithANewerCapture(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`)
	older := time.Now().Add(-time.Minute)
	writeCapture(t, env.sessionDir, capturedLogin(1, "real-refresh-stale", older))
	o := promoteOptions{sessionDir: env.sessionDir, provider: "testlogin", name: "work", store: env.store, client: env.endpoint.srv.Client()}
	_, err := promoteLogin(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "could not refresh") {
		t.Fatalf("a refused refresh with no newer capture must fail clearly: %v", err)
	}
	if login, _ := env.store.GetOAuth("work"); login.Failure == "" {
		t.Fatal("the refused login must be marked failed")
	}
	env.endpoint.mu.Lock()
	env.endpoint.accept = map[string]bool{"real-refresh-rotated": true}
	env.endpoint.refreshes = nil
	env.endpoint.mu.Unlock()
	o.force = true
	o.afterBind = func() {
		rotated := capturedLogin(1, "real-refresh-stale", older)
		for k, v := range capturedLogin(2, "real-refresh-rotated", time.Now()) {
			rotated[k] = v
		}
		writeCapture(t, env.sessionDir, rotated)
	}
	if _, err := promoteLogin(context.Background(), o); err != nil {
		t.Fatalf("promote must retry with the capture the session's proxy rotated meanwhile: %v", err)
	}
	if got := env.endpoint.refreshes; len(got) != 2 || got[0] != "real-refresh-stale" || got[1] != "real-refresh-rotated" {
		t.Fatalf("refreshes = %v", got)
	}
	login, _ := env.store.GetOAuth("work")
	if login.Failure != "" || login.Tokens["refresh_token"] != "real-refresh-101" {
		t.Fatalf("after promote: %+v", login)
	}
}

func TestPromoteRequiresCapturedTokens(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`)
	_, err := promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, provider: "testlogin", name: "work", store: env.store, client: env.endpoint.srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "has no captured login") {
		t.Fatalf("err = %v", err)
	}
	writeCapture(t, env.sessionDir, map[string]capturedToken{"rt-" + marker("b"): {Provider: "openai", Field: "refresh_token", Value: "x"}, "x-" + marker("c"): {Provider: "openai", Field: "access_token", Value: "y"}})
	_, err = promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, store: env.store, client: env.endpoint.srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "no refresh section") {
		t.Fatalf("a provider without refresh must be refused: %v", err)
	}
}

func TestPromoteChoosesTheCapturedProvider(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`, "real-refresh-1")
	writeCapture(t, env.sessionDir, capturedLogin(1, "real-refresh-1", time.Now()))
	_, err := promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, provider: "openai", store: env.store, client: env.endpoint.srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "it captured testlogin (try: hsh oauth promote testlogin)") {
		t.Fatalf("a provider the session did not capture must point at the one it did: %v", err)
	}
	out, err := promoteLogin(context.Background(), promoteOptions{sessionDir: env.sessionDir, store: env.store, client: env.endpoint.srv.Client()})
	if err != nil {
		t.Fatalf("with one captured provider, PROVIDER is optional: %v", err)
	}
	if !strings.Contains(out, "hsh://testlogin-login") {
		t.Fatalf("output:\n%s", out)
	}
	if got, err := choosePromoteProvider("", []string{"a", "b"}, "s"); err == nil || !strings.Contains(err.Error(), "captured logins for a, b") {
		t.Fatalf("two captured providers must be named: %q %v", got, err)
	}
}

func TestOAuthCompanionRefreshes(t *testing.T) {
	env := setupPhase2(t, `{"version": 1, "secrets": {"LOGIN": {"from": "hsh://work"}}}`, "real-refresh-1")
	login := config.OAuthSecret{Name: "work", Provider: "testlogin", Tokens: map[string]string{"access_token": phase2JWT(0), "id_token": phase2JWT(0), "refresh_token": "real-refresh-1"}, ExpiresAt: time.Now().Add(5 * time.Minute)}
	if err := env.store.PutOAuth(login, false); err != nil {
		t.Fatal(err)
	}
	c := newOAuthCompanion(env.sessionDir, filepath.Join(env.sessionDir, "config.json"), env.storePath)
	c.client = env.endpoint.srv.Client()
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 0 {
		t.Fatal("an idle session (no secrets.json) must not refresh")
	}
	if err := os.WriteFile(filepath.Join(env.sessionDir, "secrets.json"), []byte(`{"secrets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 1 {
		t.Fatalf("a token inside the margin must be refreshed once, got %v", env.endpoint.refreshes)
	}
	rs, _ := readSecretsJSON(t, env.sessionDir)
	if len(rs.OAuthSecrets) != 1 || !strings.HasSuffix(rs.OAuthSecrets[0].AccessToken, ".real-sig-1") {
		t.Fatalf("secrets.json must carry the refreshed access token: %+v", rs.OAuthSecrets)
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 1 {
		t.Fatalf("a fresh token must not be refreshed again, got %v", env.endpoint.refreshes)
	}
	markerPath := refreshWantedPath(env.sessionDir, "work")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte("401\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 1 || fileExists(markerPath) {
		t.Fatalf("a refresh-wanted marker right after a refresh is consumed without spending the token: %v", env.endpoint.refreshes)
	}
	l, _ := env.store.GetOAuth("work")
	l.RefreshedAt = time.Now().Add(-10 * time.Minute)
	if err := env.store.PutOAuth(l, true); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(markerPath, []byte("401\n"), 0o600)
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 2 {
		t.Fatalf("a refresh-wanted marker must force a refresh: %v", env.endpoint.refreshes)
	}
	env.endpoint.mu.Lock()
	env.endpoint.accept = map[string]bool{}
	env.endpoint.mu.Unlock()
	l, _ = env.store.GetOAuth("work")
	l.ExpiresAt = time.Now().Add(time.Minute)
	_ = env.store.PutOAuth(l, true)
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs, _ = readSecretsJSON(t, env.sessionDir)
	if rs.OAuthSecrets[0].Failure == "" {
		t.Fatalf("a refused refresh must reach the proxy as a failure: %+v", rs.OAuthSecrets[0])
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 3 {
		t.Fatalf("a failed login must not be retried every tick: %v", env.endpoint.refreshes)
	}
}

func TestRefreshStoredLogin(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`, "real-refresh-1")
	login := config.OAuthSecret{Name: "work", Provider: "testlogin", Tokens: map[string]string{"access_token": phase2JWT(0), "id_token": phase2JWT(0), "refresh_token": "real-refresh-1"}, ExpiresAt: time.Now().Add(9 * time.Hour)}
	if err := env.store.PutOAuth(login, false); err != nil {
		t.Fatal(err)
	}
	sessions := t.TempDir()
	mk := func(id, cfg string, active bool) string {
		dir := filepath.Join(sessions, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if active {
			if err := os.WriteFile(filepath.Join(dir, "secrets.json"), []byte(`{"secrets":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	uses := `{"version": 1, "secrets": {"LOGIN": {"from": "hsh://work"}}}`
	active := mk("aaa111", uses, true)
	idle := mk("bbb222", uses, false)
	other := mk("ccc333", `{"version": 1}`, true)
	out, err := refreshStoredLogin(context.Background(), env.store, "work", env.endpoint.srv.Client(), sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.endpoint.refreshes) != 1 || env.endpoint.refreshes[0] != "real-refresh-1" {
		t.Fatalf("refreshes = %v", env.endpoint.refreshes)
	}
	if !strings.Contains(out, "refreshed work (testlogin)") || !strings.Contains(out, "aaa111") || strings.Contains(out, "ccc333") {
		t.Fatalf("output:\n%s", out)
	}
	rs, raw := readSecretsJSON(t, active)
	if len(rs.OAuthSecrets) != 1 || !strings.HasSuffix(rs.OAuthSecrets[0].AccessToken, ".real-sig-1") || strings.Contains(raw, "real-refresh") {
		t.Fatalf("the active session must get the new access token (and never the refresh token): %s", raw)
	}
	if fileExists(filepath.Join(idle, "secrets.json")) {
		t.Fatal("an idle session must not be given a secrets.json")
	}
	if _, raw := readSecretsJSON(t, other); strings.Contains(raw, "oauth_secrets") {
		t.Fatalf("a session that does not use the login must be left alone: %s", raw)
	}
	env.endpoint.mu.Lock()
	env.endpoint.accept = map[string]bool{}
	env.endpoint.mu.Unlock()
	_, err = refreshStoredLogin(context.Background(), env.store, "work", env.endpoint.srv.Client(), sessions)
	if err == nil || !strings.Contains(err.Error(), "now marked failed") || !strings.Contains(err.Error(), "hsh oauth promote --force testlogin work") {
		t.Fatalf("a refused refresh must say what to do: %v", err)
	}
	if rs, _ := readSecretsJSON(t, active); rs.OAuthSecrets[0].Failure == "" {
		t.Fatal("the failure must reach the running session's proxy")
	}
	if _, err := refreshStoredLogin(context.Background(), env.store, "nope", env.endpoint.srv.Client(), sessions); err == nil || !strings.Contains(err.Error(), "no OAuth login named nope") {
		t.Fatalf("unknown login: %v", err)
	}
}

func TestUnusedLoginHints(t *testing.T) {
	env := setupPhase2(t, `{"version": 1}`)
	for _, n := range []string{"work", "spare"} {
		if err := env.store.PutOAuth(config.OAuthSecret{Name: n, Provider: "testlogin", Tokens: map[string]string{"access_token": "a", "refresh_token": "r"}, ExpiresAt: time.Now().Add(time.Hour)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.store.PutOAuth(config.OAuthSecret{Name: "claude", Provider: "anthropic", Tokens: map[string]string{"access_token": "a", "refresh_token": "r"}, ExpiresAt: time.Now().Add(time.Hour)}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.FromImageLabel(`{"version": 1, "oauth": {"providers": ["testlogin"]}, "secrets": {"LOGIN": {"from": "hsh://work", "seed": ["creds"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	hints := unusedLoginHints(cfg, env.store)
	if len(hints) != 1 || !strings.Contains(hints[0], "hsh://spare (testlogin)") || !strings.Contains(hints[0], `"seed": ["creds"]`) {
		t.Fatalf("hints = %v", hints)
	}
	cfg, _ = config.FromImageLabel(`{"version": 1}`)
	if hints := unusedLoginHints(cfg, env.store); len(hints) != 0 {
		t.Fatalf("a session that names no provider gets no hint: %v", hints)
	}
}

func refreshWantedPath(sessionDir, secret string) string {
	return filepath.Join(sessionDir, "proxy", "refresh-wanted", url.PathEscape(secret))
}
