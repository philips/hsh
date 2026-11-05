package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hsh/internal/state"
)

func resolveSession(explicit, retry string) (string, error) {
	if explicit != "" {
		id, err := resolveSessionRef(explicit)
		if err != nil {
			return "", err
		}
		if !podExists("hsh-" + id) {
			return "", fmt.Errorf("session %s does not exist", id)
		}
		return id, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	pods, err := listSandboxes("label=hsh.workspace=" + cwd)
	if err != nil {
		return "", err
	}
	switch len(pods) {
	case 0:
		return "", fmt.Errorf("no session for workspace %s; start one with hsh run (or pass --session)", filepath.Base(cwd))
	case 1:
		return strings.TrimPrefix(pods[0].Name, "hsh-"), nil
	default:
		var ids []string
		for _, p := range pods {
			id := strings.TrimPrefix(p.Name, "hsh-")
			if n := state.ReadName(id); n != "" {
				id += " (" + n + ")"
			}
			ids = append(ids, id)
		}
		return "", fmt.Errorf("multiple sessions for workspace %s:\n  %s\npick one with: %s", cwd, strings.Join(ids, "\n  "), retry)
	}
}
