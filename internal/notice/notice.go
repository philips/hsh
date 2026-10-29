package notice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	Dir         = "/hsh-run/notice"
	FileName    = "network-policy.md"
	Path        = Dir + "/" + FileName
	Placeholder = "{network_policy}"
	BlockBegin  = "<!-- hsh:network-policy begin -->"
	BlockEnd    = "<!-- hsh:network-policy end -->"
)

const Text = `# Network access (sandbox)

Some network requests are blocked on purpose by a security egress proxy. A
blocked request fails with HTTP 403 Forbidden or a refused DNS lookup. This is
not an outage. The user is notified automatically of the requested hostname.
When a request is blocked, stop, tell the user which host you need and why,
then continue with what you can do without access. Don't try other tools,
mirrors, proxies, or DNS tricks.
`

func Enabled(notice bool, mode string) bool {
	return notice && mode != "none"
}

func SessionDir(sessionDir string) string {
	return filepath.Join(sessionDir, "notice")
}

func Write(dir string, enabled bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(Text), 0o644)
}

func Read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func SubstituteArgv(argv []string, path string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		switch {
		case a == Placeholder:
			if path != "" {
				out = append(out, path)
				continue
			}
			if n := len(out); n > 0 && strings.HasPrefix(out[n-1], "-") {
				out = out[:n-1]
			}
		case strings.HasSuffix(a, "="+Placeholder):
			if path != "" {
				out = append(out, strings.TrimSuffix(a, Placeholder)+path)
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func ApplyBlock(content, text string) string {
	before, after, found := cutBlock(content)
	if text == "" {
		if !found {
			return content
		}
		return joinAround(before, after)
	}
	block := BlockBegin + "\n" + strings.TrimRight(text, "\n") + "\n" + BlockEnd + "\n"
	if !found {
		if content == "" {
			return block
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + "\n" + block
	}
	return before + block + after
}

func cutBlock(content string) (before, after string, found bool) {
	start := strings.Index(content, BlockBegin)
	if start < 0 {
		return content, "", false
	}
	rel := strings.Index(content[start:], BlockEnd)
	if rel < 0 {
		return content, "", false
	}
	end := start + rel + len(BlockEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return content[:start], content[end:], true
}

func joinAround(before, after string) string {
	if after != "" {
		return before + after
	}
	trimmed := strings.TrimRight(before, "\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

func ApplyContextFile(path, text string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil && text == "" {
		return nil
	}
	updated := ApplyBlock(string(data), text)
	if updated == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".hsh-tmp"
	if err := os.WriteFile(tmp, []byte(updated), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
