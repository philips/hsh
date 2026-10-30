package config

import (
	"net/url"
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
