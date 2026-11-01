package proxy

import (
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

	"hsh/internal/oauthprov"
)

func fakeJWT(claims map[string]any, sig string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims) + "." + sig
}

type shapedUpstream struct {
	srv       *httptest.Server
	mu        sync.Mutex
	gen       int
	refreshes []string
	contentTy []string
	lastAuth  string
}

func (u *shapedUpstream) tokens() (access, id, refresh, apiKey string) {
	claims := map[string]any{
		"email":                       "user@example.com",
		"exp":                         1900000000 + u.gen,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
	}
	return fakeJWT(claims, fmt.Sprintf("real-access-sig-%d", u.gen)),
		fakeJWT(claims, fmt.Sprintf("real-id-sig-%d", u.gen)),
		fmt.Sprintf("real-refresh-%d", u.gen),
		fmt.Sprintf("real-apikey-%d", u.gen)
}

func newShapedUpstream(t *testing.T) *shapedUpstream {
	t.Helper()
	u := &shapedUpstream{gen: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		u.contentTy = append(u.contentTy, r.Header.Get("Content-Type"))
		var req map[string]string
		if err := json.Unmarshal(body, &req); err != nil {
			vals, _ := url.ParseQuery(string(body))
			req = map[string]string{}
			for k := range vals {
				req[k] = vals.Get(k)
			}
		}
		access, id, refresh, apiKey := u.tokens()
		w.Header().Set("Content-Type", "application/json")
		switch req["grant_type"] {
		case "authorization_code":
			fmt.Fprintf(w, `{"access_token":%q,"id_token":%q,"refresh_token":%q,"expires_in":3600}`, access, id, refresh)
		case "refresh_token":
			u.refreshes = append(u.refreshes, req["refresh_token"])
			if req["refresh_token"] != refresh {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			u.gen++
			access, id, refresh, _ = u.tokens()
			fmt.Fprintf(w, `{"access_token":%q,"id_token":%q,"refresh_token":%q,"expires_in":3600}`, access, id, refresh)
		case "urn:ietf:params:oauth:grant-type:token-exchange":
			if req["subject_token"] != id {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_subject"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":%q}`, apiKey)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.lastAuth = r.Header.Get("Authorization")
		access, _, _, apiKey := u.tokens()
		if u.lastAuth != "Bearer "+access && u.lastAuth != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "api-ok")
	})
	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	return u
}

func proxyClient(addr string) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
		return url.Parse("http://" + addr)
	}}}
}

func postToken(t *testing.T, client *http.Client, target, contentType, body string) (int, map[string]any) {
	t.Helper()
	resp, err := client.Post(target, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &doc)
	return resp.StatusCode, doc
}

func apiStatus(t *testing.T, client *http.Client, target, bearer string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func jwtParts(t *testing.T, s string) []string {
	t.Helper()
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", s)
	}
	return parts
}

func TestOAuthJWTPhantomsAndJSONRefresh(t *testing.T) {
	dir := t.TempDir()
	up := newShapedUpstream(t)
	host := upstreamHost(up.srv.URL)
	prov := oauthprov.Resolved{
		Name:               "codexlike",
		TokenEndpoint:      oauthprov.Endpoint{Host: host, Path: "/token"},
		ResponseFields:     []oauthprov.Field{{Path: "access_token", Kind: "jwt"}, {Path: "id_token", Kind: "jwt"}, {Path: "refresh_token"}},
		RequestNonceFields: []string{"refresh_token", "subject_token"},
		APIHosts:           []string{host},
		Header:             "Authorization",
		Prefix:             "Bearer ",
	}
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil, prov)
	client := proxyClient(addr)

	code, login := postToken(t, client, up.srv.URL+"/token", "application/x-www-form-urlencoded", "grant_type=authorization_code&code=c&code_verifier=v")
	if code != 200 {
		t.Fatalf("login status %d: %v", code, login)
	}
	access, _ := login["access_token"].(string)
	id, _ := login["id_token"].(string)
	refresh, _ := login["refresh_token"].(string)
	realAccess, realID, _, _ := func() (string, string, string, string) {
		up.mu.Lock()
		defer up.mu.Unlock()
		up.gen = 1
		return up.tokens()
	}()
	ap, rp := jwtParts(t, access), jwtParts(t, realAccess)
	if ap[0] != rp[0] || ap[1] != rp[1] {
		t.Fatal("the access token phantom must keep the real header and claims")
	}
	if !isPhantom(ap[2]) {
		t.Fatalf("the signature segment must be an hsh_ marker, got %q", ap[2])
	}
	if jwtParts(t, id)[1] != jwtParts(t, realID)[1] || !isPhantom(jwtParts(t, id)[2]) {
		t.Fatalf("id_token phantom = %q", id)
	}
	if !isPhantom(refresh) {
		t.Fatalf("refresh phantom = %q", refresh)
	}
	if strings.Contains(access+id+refresh, "real-") {
		t.Fatal("real token material leaked to the sandbox")
	}
	if got := apiStatus(t, client, up.srv.URL+"/api", access); got != 200 {
		t.Fatalf("api with a JWT phantom: %d (upstream saw %q)", got, up.lastAuth)
	}

	code, exch := postToken(t, client, up.srv.URL+"/token", "application/x-www-form-urlencoded",
		url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {id}}.Encode())
	if code != 200 {
		t.Fatalf("token exchange with a phantom id_token: %d %v", code, exch)
	}
	apiKey, _ := exch["access_token"].(string)
	if !isPhantom(apiKey) {
		t.Fatalf("a non-JWT value in a jwt field must get an opaque phantom, got %q", apiKey)
	}
	if got := apiStatus(t, client, up.srv.URL+"/api", apiKey); got != 200 {
		t.Fatalf("api with the exchanged key phantom: %d", got)
	}

	refreshBody, _ := json.Marshal(map[string]string{"client_id": "app", "grant_type": "refresh_token", "refresh_token": refresh})
	code, rot := postToken(t, client, up.srv.URL+"/token", "application/json", string(refreshBody))
	if code != 200 {
		t.Fatalf("JSON refresh with a phantom: %d %v (upstream saw %v)", code, rot, up.refreshes)
	}
	if len(up.refreshes) != 1 || up.refreshes[0] != "real-refresh-1" {
		t.Fatalf("upstream refresh tokens = %v, want the real one", up.refreshes)
	}
	access2, _ := rot["access_token"].(string)
	if access2 == access || !isPhantom(jwtParts(t, access2)[2]) {
		t.Fatalf("rotated access phantom = %q", access2)
	}
	if got := apiStatus(t, client, up.srv.URL+"/api", access2); got != 200 {
		t.Fatalf("api with the rotated phantom: %d", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "oauth-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var store map[string]storedToken
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatal(err)
	}
	if st, ok := store[access]; !ok || st.Value != realAccess {
		t.Fatalf("the store must key the full presented phantom: %+v", st)
	}
}

func TestOAuthFormatPhantoms(t *testing.T) {
	dir := t.TempDir()
	up := newShapedUpstream(t)
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
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil, prov)
	client := proxyClient(addr)
	body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": "c"})
	_, login := postToken(t, client, up.srv.URL+"/token", "application/json", string(body))
	access, _ := login["access_token"].(string)
	refresh, _ := login["refresh_token"].(string)
	if !strings.HasPrefix(access, "sk-ant-oat01-hsh_") || !isPhantom(strings.TrimPrefix(access, "sk-ant-oat01-")) {
		t.Fatalf("access phantom = %q", access)
	}
	if !strings.HasPrefix(refresh, "sk-ant-ort01-hsh_") {
		t.Fatalf("refresh phantom = %q", refresh)
	}
	if got := apiStatus(t, client, up.srv.URL+"/api", access); got != 200 {
		t.Fatalf("api with a formatted phantom: %d (upstream saw %q)", got, up.lastAuth)
	}
	rb, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "client_id": "c"})
	code, rot := postToken(t, client, up.srv.URL+"/token", "application/json", string(rb))
	if code != 200 || len(up.refreshes) != 1 || up.refreshes[0] != "real-refresh-1" {
		t.Fatalf("formatted refresh: %d %v, upstream saw %v", code, rot, up.refreshes)
	}
	if a, _ := rot["access_token"].(string); !strings.HasPrefix(a, "sk-ant-oat01-hsh_") {
		t.Fatalf("rotated access phantom = %q", a)
	}
}

func TestOAuthPhantomScopedToItsProvider(t *testing.T) {
	dir := t.TempDir()
	up := newShapedUpstream(t)
	host := upstreamHost(up.srv.URL)
	capturer := oauthprov.Resolved{
		Name:           "capturer",
		TokenEndpoint:  oauthprov.Endpoint{Host: host, Path: "/token"},
		ResponseFields: []oauthprov.Field{{Path: "access_token", Kind: "jwt"}},
		APIHosts:       []string{"elsewhere.example"},
		Header:         "Authorization",
		Prefix:         "Bearer ",
	}
	other := oauthprov.Resolved{
		Name:           "other",
		TokenEndpoint:  oauthprov.Endpoint{Host: "auth.other.example", Path: "/token"},
		ResponseFields: []oauthprov.Field{{Path: "access_token"}},
		APIHosts:       []string{host},
		Header:         "Authorization",
		Prefix:         "Bearer ",
	}
	addr, _ := startTestProxy(t, dir, writeConfig(t, dir, up.srv.URL), nil, capturer, other)
	client := proxyClient(addr)
	_, login := postToken(t, client, up.srv.URL+"/token", "application/x-www-form-urlencoded", "grant_type=authorization_code&code=c")
	access, _ := login["access_token"].(string)
	if !isPhantom(jwtParts(t, access)[2]) {
		t.Fatalf("access phantom = %q", access)
	}
	if got := apiStatus(t, client, up.srv.URL+"/api", access); got != 401 {
		t.Fatalf("a phantom must only resolve on its own provider's api hosts; got %d (upstream saw %q)", got, up.lastAuth)
	}
	if !strings.Contains(up.lastAuth, "hsh_") {
		t.Fatalf("the phantom must reach a foreign host unresolved, upstream saw %q", up.lastAuth)
	}
}

func TestIsJSONBody(t *testing.T) {
	cases := []struct {
		ct, body string
		want     bool
	}{
		{"application/json", `{"a":1}`, true},
		{"application/json; charset=utf-8", `{}`, true},
		{"application/vnd.api+json", `{}`, true},
		{"", ` {"grant_type":"refresh_token"}`, true},
		{"application/x-www-form-urlencoded", "grant_type=refresh_token", false},
		{"", "grant_type=refresh_token", false},
	}
	for _, c := range cases {
		if got := isJSONBody([]byte(c.body), c.ct); got != c.want {
			t.Errorf("isJSONBody(%q, %q) = %v", c.body, c.ct, got)
		}
	}
}
