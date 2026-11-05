package config

import "testing"

func TestHostPattern(t *testing.T) {
	cases := map[string]string{
		"example.com":                "example.com",
		"  Example.COM. ":            "example.com",
		"*.example.com":              "*.example.com",
		"https://api.x.io:8443/v1?q": "api.x.io",
		"api.x.io:8443":              "api.x.io",
		"api.x.io/path":              "api.x.io",
	}
	for in, want := range cases {
		if got, err := HostPattern(in); err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "*.", "a b.com", "*.*.example.com", "..example.com", ".example.com"} {
		if got, err := HostPattern(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}
