package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hsh/internal/denieddb"
)

// podInfo is one hsh sandbox pod, as reported by
// `podman pod ls --format '{{json .}}'`.
type podInfo struct {
	ID         string `json:"Id"`
	Name       string `json:"Name"`
	Status     string `json:"Status"`
	Created    string `json:"Created"`
	InfraID    string `json:"InfraId"`
	Containers []struct {
		ID     string `json:"Id"`
		Names  string `json:"Names"`
		Status string `json:"Status"`
	} `json:"Containers"`
	Labels map[string]string `json:"Labels"`
}

func (p podInfo) Workspace() string { return p.Labels["hsh.workspace"] }

func (p podInfo) NumContainers() int { return len(p.Containers) }

func (p podInfo) CreatedTime() time.Time {
	t, err := time.Parse(time.RFC3339, p.Created)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isSandbox reports whether the pod was created by hsh (pods are named hsh-*).
func (p podInfo) isSandbox() bool { return strings.HasPrefix(p.Name, "hsh-") }

// listSandboxes lists hsh sandbox pods. extraFilters are passed to podman as
// additional --filter arguments.
func listSandboxes(extraFilters ...string) ([]podInfo, error) {
	args := []string{"pod", "ls", "--filter", "name=hsh-", "--format", "{{json .}}"}
	for _, f := range extraFilters {
		args = append(args, "--filter", f)
	}
	out, err := podman(args...)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w\n%s", err, out)
	}
	var pods []podInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p podInfo
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("parse pod list entry: %w", err)
		}
		pods = append(pods, p)
	}
	return pods, nil
}

// sandboxPods filters a pod list down to hsh sandboxes (name prefix hsh-).
func sandboxPods(pods []podInfo) []podInfo {
	var out []podInfo
	for _, p := range pods {
		if p.isSandbox() {
			out = append(out, p)
		}
	}
	return out
}

// sessionDirFor returns the --resume session state directory for a sandbox
// pod (the pod name suffix is the workspace hash).
func sessionDirFor(podName string) string {
	return filepath.Join(denieddb.StateDir(), "sessions", strings.TrimPrefix(podName, "hsh-"))
}

// removeSessionDir removes the sandbox's session state directory if present.
// It returns true when a directory was removed.
func removeSessionDir(podName string) (bool, error) {
	dir := sessionDirFor(podName)
	if _, err := os.Stat(dir); err != nil {
		return false, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

// sweepSessionDirs removes every session state directory (used by rm --all,
// including orphaned ones whose pod is already gone).
func sweepSessionDirs() (int, error) {
	base := filepath.Join(denieddb.StateDir(), "sessions")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
