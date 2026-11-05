package config

import (
	"fmt"
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
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range list {
		e := strings.ToLower(strings.TrimSuffix(entry, "."))
		if e == host {
			return true
		}
		if strings.HasPrefix(e, "*.") {
			suffix := e[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
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

func HostPattern(input string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(input))
	if v == "" {
		return "", fmt.Errorf("type a host, *.domain, or URL")
	}
	wildcard := strings.HasPrefix(v, "*.")
	host := v
	if wildcard {
		host = v[2:]
	} else {
		host = HostFromRef(v)
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.ContainsAny(host, " \t*/@?#") || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", fmt.Errorf("%q is not a host, *.domain, or URL", input)
	}
	if wildcard {
		return "*." + host, nil
	}
	return host, nil
}
