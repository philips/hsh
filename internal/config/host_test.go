package config

import (
	"strings"
	"testing"
)

func TestPatternGrammar(t *testing.T) {
	cases := map[string]bool{
		"example.com":                     true,
		"*.example.com":                   true,
		"access-analyzer.*.amazonaws.com": true,
		"api*.example.com":                true,
		"web*-canary.example.com":         true,
		"web.qa-*.internal.example.com":   true,
		"foo*.github.io":                  true,
		"10.0.0.1":                        true,
		"::1":                             true,
		"[2001:db8::1]":                   true,
		"localhost":                       true,
		"xn--exmple-cua.com":              true,
		"Example.COM.":                    true,
		"*":                               false,
		"*.com":                           false,
		"login.*.com":                     false,
		"*.*.example.com":                 false,
		"*.api*.example.com":              false,
		"*example.com":                    false,
		"*-canary.example.com":            false,
		"ex*ample.com":                    false,
		"éxämple.com":                     false,
		"example.*":                       false,
		"*.0.0.1":                         false,
		"":                                false,
		"a..example.com":                  false,
		".example.com":                    false,
		"a b.example.com":                 false,
		"user@example.com":                false,
		"example.com:443":                 false,
		"fe80::1%eth0":                    false,
		strings.Repeat("a", 64) + ".com":  false,
	}
	for p, want := range cases {
		_, err := CanonicalPattern(p)
		if (err == nil) != want {
			t.Errorf("%q: valid=%v want %v (%v)", p, err == nil, want, err)
		}
	}
}

func TestCanonicalPatternNormalizes(t *testing.T) {
	cases := map[string]string{
		"Example.COM.":     "example.com",
		"*.Example.com":    "*.example.com",
		"[2001:DB8::0:1]":  "2001:db8::1",
		"API*.example.com": "api*.example.com",
	}
	for in, want := range cases {
		if got, err := CanonicalPattern(in); err != nil || got != want {
			t.Errorf("%q: %q %v want %q", in, got, err, want)
		}
	}
	if _, err := CanonicalPattern("*.com"); err == nil || !strings.Contains(err.Error(), "two literal labels") {
		t.Errorf("the error does not say why: %v", err)
	}
	if _, err := CanonicalPattern("éxämple.com"); err == nil || !strings.Contains(err.Error(), "Punycode") {
		t.Errorf("the error does not mention Punycode: %v", err)
	}
}

func TestPatternMatching(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "EXAMPLE.com.", true},
		{"example.com", "api.example.com", false},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "badexample.com", false},
		{"access-analyzer.*.amazonaws.com", "access-analyzer.us-west-2.amazonaws.com", true},
		{"access-analyzer.*.amazonaws.com", "access-analyzer.a.b.amazonaws.com", false},
		{"access-analyzer.*.amazonaws.com", "other.us-west-2.amazonaws.com", false},
		{"api*.example.com", "api-east.example.com", true},
		{"api*.example.com", "api.example.com", false},
		{"api*.example.com", "api.a.example.com", false},
		{"api*.example.com", "a.api-east.example.com", false},
		{"web*-canary.example.com", "web1-canary.example.com", true},
		{"web*-canary.example.com", "web-canary.example.com", false},
		{"web*-canary.example.com", "web1-canary-x.example.com", false},
		{"web.qa-*.internal.example.com", "web.qa-7.internal.example.com", true},
		{"web.qa-*.internal.example.com", "web.qa-.internal.example.com", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.0.0.1", "10.0.0.10", false},
		{"[2001:db8::1]", "2001:db8:0::1", true},
		{"*example.com", "badexample.com", false},
		{"*.com", "example.com", false},
		{"login.*.com", "login.evil.com", false},
	}
	for _, c := range cases {
		if got := HostMatches([]string{c.pattern}, c.host); got != c.want {
			t.Errorf("%s ~ %s: %v want %v", c.pattern, c.host, got, c.want)
		}
	}
}

func TestValidHostname(t *testing.T) {
	for _, h := range []string{"example.com", "API.example.com.", "host.containers.internal", "localhost", "10.0.0.1", "::1", "[::1]", "_svc.example.com"} {
		if !ValidHostname(h) {
			t.Errorf("%q rejected", h)
		}
	}
	for _, h := range []string{"", "*.example.com", "a..b", "bad\x1b[2J", "a b", "éxämple.com", "example.com:443", strings.Repeat("a.", 130) + "com"} {
		if ValidHostname(h) {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestHostPattern(t *testing.T) {
	cases := map[string]string{
		"example.com":                "example.com",
		"  Example.COM. ":            "example.com",
		"*.example.com":              "*.example.com",
		"api*.Example.com":           "api*.example.com",
		"https://api.x.io:8443/v1?q": "api.x.io",
		"api.x.io:8443":              "api.x.io",
		"api.x.io/path":              "api.x.io",
		"http://[::1]:8080/":         "::1",
	}
	for in, want := range cases {
		if got, err := HostPattern(in); err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "*.", "a b.com", "*.*.example.com", "..example.com", ".example.com", "*.com", "user@host"} {
		if got, err := HostPattern(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
	if _, err := HostPattern("*.com"); err == nil || !strings.Contains(err.Error(), `"*.com" is not a host pattern or URL: a wildcard needs`) {
		t.Errorf("error = %v", err)
	}
}

func TestAnInvalidAllowEntryIsALoadError(t *testing.T) {
	if _, err := FromImageLabel(`{"version":1,"network":{"allow":["ok.example","*.com"]}}`); err == nil || !strings.Contains(err.Error(), `network.allow: invalid host pattern "*.com"`) {
		t.Fatalf("err = %v", err)
	}
	cfg, err := FromImageLabel(`{"version":1,"network":{"allow":["Ok.Example.","*.ok.example","10.0.0.1"]}}`)
	if err != nil || len(cfg.Network.Allow) != 3 {
		t.Fatalf("%v %v", cfg, err)
	}
}
