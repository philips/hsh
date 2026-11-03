package oauthprov

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testJWT(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256"}) + "." + enc(claims) + ".real-sig"
}

func TestPresent(t *testing.T) {
	marker := "hsh_" + strings.Repeat("a", 64)
	jwt := testJWT(map[string]any{"exp": 1900000000})
	if got := Present(Field{Path: "access_token", Kind: "jwt"}, jwt, marker); got != strings.TrimSuffix(jwt, "real-sig")+marker {
		t.Fatalf("jwt = %q", got)
	}
	if got := Present(Field{Path: "access_token", Kind: "jwt"}, "sk-not-a-jwt", marker); got != marker {
		t.Fatalf("non-JWT in a jwt field = %q", got)
	}
	if got := Present(Field{Path: "access_token", Format: "sk-ant-oat01-{phantom}"}, "x", marker); got != "sk-ant-oat01-"+marker {
		t.Fatalf("format = %q", got)
	}
	if got := Present(Field{Path: "refresh_token"}, "x", marker); got != marker {
		t.Fatalf("opaque = %q", got)
	}
	if !MarkerPattern.MatchString(Present(Field{Kind: "jwt"}, jwt, marker)) {
		t.Fatal("presented values must carry a findable marker")
	}
}

func TestParseTokenResponse(t *testing.T) {
	now := time.Unix(1800000000, 0)
	fields := []Field{{Path: "access_token", Kind: "jwt"}, {Path: "refresh_token"}}
	doc := map[string]any{
		"access_token":  testJWT(map[string]any{"exp": 1800009999}),
		"refresh_token": "r",
		"accessToken":   "never-meta",
		"expires_in":    json.Number("3600"),
		"scope":         "user:inference user:profile",
		"token_type":    "Bearer",
		"account":       map[string]any{"email": "x"},
	}
	r := ParseTokenResponse(doc, fields, "", now)
	if r.Tokens["refresh_token"] != "r" || r.Tokens["access_token"] == "" {
		t.Fatalf("tokens = %v", r.Tokens)
	}
	if !r.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires_in must win: %v", r.ExpiresAt)
	}
	if r.Meta["scope"] != "user:inference user:profile" || r.Meta["token_type"] != "Bearer" {
		t.Fatalf("meta = %v", r.Meta)
	}
	if _, leaked := r.Meta["accessToken"]; leaked {
		t.Fatal("token-like fields must never become meta")
	}
	delete(doc, "expires_in")
	r = ParseTokenResponse(doc, fields, "", now)
	if !r.ExpiresAt.Equal(time.Unix(1800009999, 0)) {
		t.Fatalf("without expires_in the JWT exp is used: %v", r.ExpiresAt)
	}
}

func TestRefreshTokens(t *testing.T) {
	var gotCT string
	var gotBody map[string]string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		gotBody = map[string]string{}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			vals, _ := url.ParseQuery(string(data))
			for k := range vals {
				gotBody[k] = vals.Get(k)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":60}`))
	}))
	defer srv.Close()
	d := Descriptor{
		TokenEndpoint:  Endpoint{Host: "unused", Path: "/t"},
		ResponseFields: []Field{{Path: "access_token"}, {Path: "refresh_token"}},
		Refresh:        &Refresh{ClientID: "cid", RequestBody: "json", URL: srv.URL, Extra: map[string]string{"scope": "s"}},
	}
	now := time.Now()
	r, err := d.RefreshTokens(context.Background(), srv.Client(), "old-refresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if gotCT != "application/json" || gotBody["grant_type"] != "refresh_token" || gotBody["refresh_token"] != "old-refresh" || gotBody["client_id"] != "cid" || gotBody["scope"] != "s" {
		t.Fatalf("request = %s %v", gotCT, gotBody)
	}
	if r.Tokens["access_token"] != "new-access" || r.Tokens["refresh_token"] != "" || !r.ExpiresAt.After(now) {
		t.Fatalf("result = %+v", r)
	}
	d.Refresh.RequestBody = "form"
	if _, err := d.RefreshTokens(context.Background(), srv.Client(), "old-refresh", now); err != nil || gotCT != "application/x-www-form-urlencoded" || gotBody["refresh_token"] != "old-refresh" {
		t.Fatalf("form refresh: %v %s %v", err, gotCT, gotBody)
	}
	status = 400
	_, err = d.RefreshTokens(context.Background(), srv.Client(), "old-refresh", now)
	var re *RefreshError
	if !errors.As(err, &re) || !re.Permanent || re.Code != "invalid_grant" {
		t.Fatalf("invalid_grant must be permanent: %v", err)
	}
	status = 503
	_, err = d.RefreshTokens(context.Background(), srv.Client(), "old-refresh", now)
	if !errors.As(err, &re) || re.Permanent {
		t.Fatalf("a 503 must be transient: %v", err)
	}
	if d.RefreshURL() != srv.URL {
		t.Fatal("refresh.url overrides the token endpoint")
	}
	d.Refresh.URL = ""
	if d.RefreshURL() != "https://unused/t" {
		t.Fatalf("default refresh url = %s", d.RefreshURL())
	}
}

func TestShippedSeedsRenderJSON(t *testing.T) {
	marker := "hsh_" + strings.Repeat("b", 64)
	jwt := testJWT(map[string]any{"exp": 1900000000}) + ""
	jwtPhantom := strings.TrimSuffix(jwt, "real-sig") + marker
	for _, name := range []string{"anthropic", "openai-codex"} {
		l, err := Load(t.TempDir(), name)
		if err != nil {
			t.Fatal(err)
		}
		if l.Descriptor.Refresh == nil || len(l.Descriptor.Seeds) != 1 {
			t.Fatalf("%s: shipped descriptor needs refresh and one seed", name)
		}
		for seedName, seed := range l.Descriptor.Seeds {
			for _, meta := range []map[string]string{{"scope": "user:inference user:profile"}, {}} {
				data := map[string]any{
					"access_token":      "sk-ant-oat01-" + marker,
					"refresh_token":     marker,
					"id_token":          jwtPhantom,
					"expires_at":        "2030-01-01T00:00:00Z",
					"expires_at_millis": int64(1893456000000),
					"now":               "2026-01-01T00:00:00Z",
					"meta":              meta,
				}
				out, err := RenderSeed(seedName, seed, data)
				if err != nil {
					t.Fatalf("%s/%s: %v", name, seedName, err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out, &doc); err != nil {
					t.Fatalf("%s/%s renders invalid JSON: %v\n%s", name, seedName, err, out)
				}
				if !strings.Contains(string(out), marker) || strings.Contains(string(out), "real") {
					t.Fatalf("%s/%s: %s", name, seedName, out)
				}
			}
		}
	}
	l, _ := Load(t.TempDir(), "anthropic")
	out, _ := RenderSeed("claude-code", l.Descriptor.Seeds["claude-code"], map[string]any{"meta": map[string]string{"scope": "a b"}, "expires_at_millis": 1})
	if !strings.Contains(string(out), `"scopes":["a","b"]`) {
		t.Fatalf("scopes from meta.scope: %s", out)
	}
}

func TestSeedAndRefreshValidation(t *testing.T) {
	base := `{"version": 1, "token_endpoint": {"host": "a", "path": "/t"}, "response_fields": ["access_token"], "api": {"hosts": ["a"]}, `
	cases := map[string]string{
		`"refresh": {"client_id": ""}}`:                                   "client_id is empty",
		`"refresh": {"client_id": "c", "request_body": "xml"}}`:           "request_body",
		`"refresh": {"client_id": "c", "url": "ftp://x"}}`:                "not an http(s) URL",
		`"seeds": {"x": {"path": "relative", "template": "{}"}}}`:         "must be absolute",
		`"seeds": {"x": {"path": "/a", "template": "{{.broken"}}}`:        "seed \"x\"",
		`"seeds": {"x": {"path": "/a", "template": "{}", "mode": "rw"}}}`: "not octal",
	}
	for tail, want := range cases {
		if _, err := Parse([]byte(base + tail)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", tail, err, want)
		}
	}
}
