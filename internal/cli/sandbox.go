package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hsh/internal/state"
)

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

func (p podInfo) Session() string { return strings.TrimPrefix(p.Name, "hsh-") }

func (p podInfo) isSandbox() bool { return strings.HasPrefix(p.Name, "hsh-") }

type stringArray []string

func (a *stringArray) String() string { return strings.Join(*a, ",") }

func (a *stringArray) Set(v string) error {
	*a = append(*a, v)
	return nil
}

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

func sandboxPods(pods []podInfo) []podInfo {
	var out []podInfo
	for _, p := range pods {
		if p.isSandbox() {
			out = append(out, p)
		}
	}
	return out
}

func latestPod(pods []podInfo) (podInfo, bool) {
	if len(pods) == 0 {
		return podInfo{}, false
	}
	best := pods[0]
	for _, p := range pods[1:] {
		if p.CreatedTime().After(best.CreatedTime()) {
			best = p
		}
	}
	return best, true
}

func sessionDirFor(podName string) string {
	return filepath.Join(state.Dir(), "sessions", strings.TrimPrefix(podName, "hsh-"))
}

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

func sweepSessionDirs() (int, error) {
	base := filepath.Join(state.Dir(), "sessions")
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

func collectGarbage() (int, error) {
	base := filepath.Join(state.Dir(), "sessions")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		id := e.Name()
		if id == "" || podExists("hsh-"+id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, id)); err != nil {
			return removed, err
		}
		fmt.Printf("removed session state %s\n", id)
		removed++
	}
	return removed, nil
}
