package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/settings"
)

const Version = 1

type Recorded struct {
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	Runs    int      `json:"runs,omitempty"`
	At      string   `json:"at,omitempty"`
	HSH     string   `json:"hsh,omitempty"`
}

type Rule struct {
	Hosts  []string `json:"hosts"`
	Access string   `json:"access,omitempty"`
	Log    string   `json:"log,omitempty"`
}

type Policy struct {
	Version     int       `json:"version"`
	Description string    `json:"description,omitempty"`
	Recorded    *Recorded `json:"recorded,omitempty"`
	Rules       []Rule    `json:"rules"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid policy name %q (lowercase letters, digits, and -, starting with a letter or digit, at most 63 characters)", name)
	}
	return nil
}

func Dir() string {
	return filepath.Join(settings.Dir(), "policy", "network")
}

func Path(name string) string {
	return filepath.Join(Dir(), name+".json")
}

func Parse(data []byte, source string) (*Policy, error) {
	var head struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	switch {
	case head.Version == nil:
		return nil, fmt.Errorf("%s: no \"version\"; a network policy starts with \"version\": %d", source, Version)
	case *head.Version > Version:
		return nil, fmt.Errorf("%s: policy version %d is newer than this hsh reads (version %d); update hsh", source, *head.Version, Version)
	case *head.Version != Version:
		return nil, fmt.Errorf("%s: policy version %d is not supported (this hsh reads version %d)", source, *head.Version, Version)
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := p.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &p, nil
}

func (p *Policy) normalize() error {
	if len(p.Rules) == 0 {
		return errors.New("rules: a policy needs at least one rule")
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		switch r.Access {
		case "", monitordb.AccessAllow, monitordb.AccessDeny:
		default:
			return fmt.Errorf("rules[%d]: access %q (want allow or deny)", i, r.Access)
		}
		switch r.Log {
		case "", monitordb.LogRecord, monitordb.LogReview:
		default:
			return fmt.Errorf("rules[%d]: log %q (want record or review)", i, r.Log)
		}
		if r.Access == "" && r.Log == "" {
			return fmt.Errorf("rules[%d]: a rule sets access, log, or both", i)
		}
		if len(r.Hosts) == 0 {
			return fmt.Errorf("rules[%d]: hosts is empty", i)
		}
		for j, h := range r.Hosts {
			c, err := config.CanonicalPattern(h)
			if err != nil {
				return fmt.Errorf("rules[%d].hosts: %w", i, err)
			}
			r.Hosts[j] = c
		}
	}
	return nil
}

func (p *Policy) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (p *Policy) SessionRules(name string) []monitordb.Rule {
	source := monitordb.PolicySource(name)
	index := map[string]int{}
	var out []monitordb.Rule
	for _, r := range p.Rules {
		for _, h := range r.Hosts {
			i, ok := index[h]
			if !ok {
				i = len(out)
				index[h] = i
				out = append(out, monitordb.Rule{Pattern: h, Source: source})
			}
			m := &out[i]
			if r.Access == monitordb.AccessDeny || (r.Access == monitordb.AccessAllow && m.Access == "") {
				m.Access = r.Access
			}
			if r.Log == monitordb.LogRecord || (r.Log == monitordb.LogReview && m.Log == "") {
				m.Log = r.Log
			}
		}
	}
	return out
}

func Load(name string) (*Policy, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no policy %q (hsh policy ls lists them; hsh policy add %s FILE adds it)", name, name)
	}
	if err != nil {
		return nil, err
	}
	return Parse(data, Path(name))
}

type Entry struct {
	Name   string
	Policy *Policy
	Err    error
}

func List() ([]Entry, error) {
	files, err := os.ReadDir(Dir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		name, ok := strings.CutSuffix(f.Name(), ".json")
		if !ok || f.IsDir() || ValidName(name) != nil {
			continue
		}
		p, err := Load(name)
		out = append(out, Entry{Name: name, Policy: p, Err: err})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

var ErrExists = errors.New("policy exists")

func Save(name string, p *Policy, force bool) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if _, err := os.Stat(Path(name)); err == nil && !force {
		return fmt.Errorf("policy %q already exists; replace it with --force: %w", name, ErrExists)
	}
	data, err := p.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(Dir(), ".policy-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, Path(name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func Remove(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	err := os.Remove(Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no policy %q", name)
	}
	return err
}
