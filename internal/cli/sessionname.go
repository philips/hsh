package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"hsh/internal/state"
)

const sessionNameEnv = "HSH_SESSION_NAME"

var (
	sessionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	sessionHexRE  = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

var sessionPodExists = podExists

func sessionNamesOnDisk() map[string][]string {
	byName := map[string][]string{}
	for id, name := range state.Names() {
		byName[name] = append(byName[name], id)
	}
	return byName
}

func validateSessionName(name, selfID string) error {
	switch {
	case name == "":
		return fmt.Errorf("session name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("invalid session name %q", name)
	case strings.Contains(name, "/"):
		return fmt.Errorf("invalid session name %q: it must not contain a path separator", name)
	case strings.HasPrefix(name, "hsh-"):
		return fmt.Errorf("invalid session name %q: it must not start with \"hsh-\"", name)
	case sessionHexRE.MatchString(name):
		return fmt.Errorf("invalid session name %q: it looks like a session id", name)
	case !sessionNameRE.MatchString(name):
		return fmt.Errorf("invalid session name %q: use 1-63 characters from a-z, 0-9, '.', '_' and '-', starting with a letter or digit", name)
	}
	for _, id := range sessionNamesOnDisk()[name] {
		if id == selfID || !sessionPodExists("hsh-"+id) {
			continue
		}
		return fmt.Errorf("session name %q is taken by session %s (re-enter it with: hsh resume %s)", name, id, name)
	}
	return nil
}

func initialSessionName(flagValue string) (string, error) {
	name, source := flagValue, "--name"
	if name == "" {
		name, source = os.Getenv(sessionNameEnv), sessionNameEnv
	}
	if name == "" {
		return "", nil
	}
	if err := validateSessionName(name, ""); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return name, nil
}

func resolveSessionRef(arg string) (string, error) {
	short := strings.TrimPrefix(arg, "hsh-")
	if strings.ContainsAny(short, "/\\") {
		return short, nil
	}
	ids := sessionNamesOnDisk()[arg]
	var live []string
	for _, id := range ids {
		if sessionPodExists("hsh-" + id) {
			live = append(live, id)
		}
	}
	switch len(live) {
	case 1:
		return live[0], nil
	case 0:
	default:
		return "", fmt.Errorf("session name %q matches sessions %s; use an id", arg, strings.Join(live, " and "))
	}
	if fileExists(state.SessionDir(short)) {
		return short, nil
	}
	entries, err := os.ReadDir(state.SessionsDir())
	if err != nil || short == "" {
		return short, nil
	}
	var matched []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), short) {
			matched = append(matched, e.Name())
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return short, nil
	}
	return "", fmt.Errorf("ambiguous session %q matches %d sessions", arg, len(matched))
}

func podsNamed(arg string, pods []podInfo) []podInfo {
	if arg == "" {
		return nil
	}
	var out []podInfo
	for _, p := range pods {
		if state.ReadName(p.Session()) == arg {
			out = append(out, p)
		}
	}
	return out
}

func nameDirectoryConflict(arg string, pods []podInfo) error {
	named := podsNamed(arg, pods)
	if len(named) == 0 {
		return nil
	}
	if _, ok := isDirectory(arg); !ok {
		return nil
	}
	return fmt.Errorf("%q is both a directory and a session name; use ./%s for the workspace or the session id %s for the session", arg, arg, named[0].Session())
}

func sessionLabel(id string) string {
	if n := state.ReadName(id); n != "" {
		return n
	}
	return id
}
