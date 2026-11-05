package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

func HostFromRef(ref string) string {
	s := ref
	if i := strings.Index(s, "://"); i >= 0 {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s, "]") {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	return s
}

func HostMatches(list []string, host string) bool {
	host = normalizeHost(host)
	for _, entry := range list {
		if patternMatches(entry, host) {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return h
}

var labelPattern = regexp.MustCompile(`^[a-z0-9_-]{1,63}$`)

var wildcardPartPattern = regexp.MustCompile(`^[a-z0-9_-]*$`)

func ipLiteral(h string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(h)
	return a, err == nil && a.Zone() == ""
}

func ValidHostname(host string) bool {
	h := normalizeHost(host)
	if h == "" || len(h) > 253 {
		return false
	}
	if _, ok := ipLiteral(h); ok {
		return true
	}
	for _, l := range strings.Split(h, ".") {
		if !labelPattern.MatchString(l) {
			return false
		}
	}
	return true
}

func CanonicalPattern(pattern string) (string, error) {
	p := normalizeHost(pattern)
	if err := checkPattern(p); err != nil {
		return "", fmt.Errorf("invalid host pattern %q: %w", pattern, err)
	}
	if a, ok := ipLiteral(p); ok {
		return a.String(), nil
	}
	return p, nil
}

type patternError string

func (e patternError) Error() string { return string(e) }

func checkPattern(p string) error {
	if p == "" {
		return patternError("empty")
	}
	if len(p) > 253 {
		return patternError("longer than 253 characters")
	}
	if _, ok := ipLiteral(p); ok {
		return nil
	}
	for _, r := range p {
		if r > 0x7f {
			return patternError("not ASCII; write internationalized names in Punycode (xn--...)")
		}
	}
	if n := strings.Count(p, "*"); n > 1 {
		return patternError("more than one wildcard")
	}
	labels := strings.Split(p, ".")
	wild := -1
	for i, l := range labels {
		if l == "" {
			return patternError("empty label")
		}
		if !strings.Contains(l, "*") {
			if !labelPattern.MatchString(l) {
				return patternError(fmt.Sprintf("label %q is not a hostname label (letters, digits, - and _)", l))
			}
			continue
		}
		wild = i
		if l == "*" {
			continue
		}
		prefix, suffix, _ := strings.Cut(l, "*")
		if prefix == "" {
			return patternError("a * inside a label needs a literal prefix (api*.example.com)")
		}
		if !wildcardPartPattern.MatchString(prefix) || !wildcardPartPattern.MatchString(suffix) || len(l) > 63 {
			return patternError(fmt.Sprintf("label %q is not a hostname label (letters, digits, - and _)", l))
		}
	}
	if wild < 0 {
		return nil
	}
	if len(labels)-wild-1 < 2 {
		return patternError("a wildcard needs at least two literal labels after it (*.example.com, not *.com)")
	}
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return patternError("a wildcard pattern cannot end in a numeric label")
	}
	return nil
}

func patternMatches(pattern, host string) bool {
	p := normalizeHost(pattern)
	if p == "" || host == "" {
		return false
	}
	if pa, ok := ipLiteral(p); ok {
		ha, ok := ipLiteral(host)
		return ok && pa == ha
	}
	if !strings.Contains(p, "*") {
		return p == host
	}
	if checkPattern(p) != nil {
		return false
	}
	if strings.HasPrefix(p, "*.") {
		suffix := p[1:]
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	pl, hl := strings.Split(p, "."), strings.Split(host, ".")
	if len(pl) != len(hl) {
		return false
	}
	for i, l := range pl {
		if !strings.Contains(l, "*") {
			if l != hl[i] {
				return false
			}
			continue
		}
		if l == "*" {
			if hl[i] == "" {
				return false
			}
			continue
		}
		prefix, suffix, _ := strings.Cut(l, "*")
		h := hl[i]
		if len(h) < len(prefix)+len(suffix)+1 || !strings.HasPrefix(h, prefix) || !strings.HasSuffix(h, suffix) {
			return false
		}
	}
	return true
}

func HostPattern(input string) (string, error) {
	v := strings.TrimSpace(input)
	if v == "" {
		return "", fmt.Errorf("type a host, *.domain, or URL")
	}
	p, err := CanonicalPattern(HostFromRef(strings.ToLower(v)))
	if err != nil {
		return "", fmt.Errorf("%q is not a host pattern or URL: %w", input, errors.Unwrap(err))
	}
	return p, nil
}

var secretHostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9_]([a-z0-9_.-]*[a-z0-9_])?$|^[0-9a-f:]+$`)

func NormalizeSecretHost(ref string) (string, error) {
	h := strings.TrimSpace(ref)
	if strings.HasPrefix(h, "*.") {
		h = "*." + HostFromRef(h[2:])
	} else {
		h = HostFromRef(h)
	}
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	if h == "" || h == "*." || !secretHostPattern.MatchString(h) || strings.Contains(h, "..") {
		return "", fmt.Errorf("invalid host %q (want a hostname such as api.example.com or *.example.com)", ref)
	}
	return h, nil
}

func HostCovered(approved []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasPrefix(host, "*.") {
		return HostMatches(approved, host)
	}
	for _, a := range approved {
		a = strings.ToLower(strings.TrimSuffix(a, "."))
		if a == host {
			return true
		}
		if strings.HasPrefix(a, "*.") && strings.HasSuffix(host[1:], a[1:]) {
			return true
		}
	}
	return false
}
