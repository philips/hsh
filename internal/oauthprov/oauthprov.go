package oauthprov

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const FormatVersion = 1

const PhantomPlaceholder = "{phantom}"

//go:embed providers/*.json
var shipped embed.FS

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type Endpoint struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

type Field struct {
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
	Format string `json:"format,omitempty"`
}

func (f *Field) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = Field{Path: s}
		return nil
	}
	type plain Field
	var p plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*f = Field(p)
	return nil
}

type API struct {
	Hosts  []string `json:"hosts"`
	Header string   `json:"header,omitempty"`
	Prefix *string  `json:"prefix,omitempty"`
}

type Descriptor struct {
	Version            int             `json:"version"`
	Base               string          `json:"base,omitempty"`
	TokenEndpoint      Endpoint        `json:"token_endpoint"`
	ResponseFields     []Field         `json:"response_fields"`
	RequestNonceFields []string        `json:"request_nonce_fields,omitempty"`
	API                API             `json:"api"`
	ExpiresIn          string          `json:"expires_in,omitempty"`
	Refresh            *Refresh        `json:"refresh,omitempty"`
	Seeds              map[string]Seed `json:"seeds,omitempty"`
}

type Resolved struct {
	Name               string   `json:"name"`
	TokenEndpoint      Endpoint `json:"token_endpoint"`
	ResponseFields     []Field  `json:"response_fields"`
	RequestNonceFields []string `json:"request_nonce_fields,omitempty"`
	APIHosts           []string `json:"api_hosts"`
	Header             string   `json:"header"`
	Prefix             string   `json:"prefix"`
	ExpiresIn          string   `json:"expires_in,omitempty"`
}

type SourceKind string

const (
	SourceShipped  SourceKind = "shipped"
	SourceOverride SourceKind = "override"
	SourceDropIn   SourceKind = "drop-in"
)

type Source struct {
	Kind SourceKind
	Path string
}

type Loaded struct {
	Name       string
	Descriptor Descriptor
	Merged     []byte
	Sources    []Source
	Warnings   []string
}

func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid provider name %q (use lowercase letters, digits, '.', '_' and '-')", name)
	}
	return nil
}

func Shipped(name string) ([]byte, bool) {
	if ValidName(name) != nil {
		return nil, false
	}
	data, err := shipped.ReadFile("providers/" + name + ".json")
	if err != nil {
		return nil, false
	}
	return data, true
}

func ShippedNames() []string {
	entries, _ := fs.ReadDir(shipped, "providers")
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(out)
	return out
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func OverridePath(dir, name string) string {
	return filepath.Join(dir, name+".json")
}

func DropInDir(dir, name string) string {
	return filepath.Join(dir, name+".d")
}

func Names(dir string) ([]string, error) {
	set := map[string]bool{}
	for _, n := range ShippedNames() {
		set[n] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case !e.IsDir() && strings.HasSuffix(n, ".json"):
			n = strings.TrimSuffix(n, ".json")
		case e.IsDir() && strings.HasSuffix(n, ".d"):
			n = strings.TrimSuffix(n, ".d")
		default:
			continue
		}
		if ValidName(n) == nil {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func Load(dir, name string) (*Loaded, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	l := &Loaded{Name: name}
	shippedData, isShipped := Shipped(name)
	var base []byte
	overridePath := OverridePath(dir, name)
	if data, err := os.ReadFile(overridePath); err == nil {
		base = data
		l.Sources = append(l.Sources, Source{Kind: SourceOverride, Path: overridePath})
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if isShipped {
		base = shippedData
		l.Sources = append(l.Sources, Source{Kind: SourceShipped, Path: "(built into hsh)"})
	}
	dropIns, err := filepath.Glob(filepath.Join(DropInDir(dir, name), "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(dropIns)
	if base == nil {
		if len(dropIns) > 0 {
			return nil, fmt.Errorf("oauth provider %q has drop-ins in %s but no descriptor to apply them to (hsh ships no %q; add %s)", name, DropInDir(dir, name), name, overridePath)
		}
		return nil, fmt.Errorf("unknown oauth provider %q (see hsh oauth provider ls)", name)
	}
	var doc any
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", l.Sources[0].Path, err)
	}
	for _, p := range dropIns {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var patch any
		if err := json.Unmarshal(data, &patch); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		doc = MergePatch(doc, patch)
		l.Sources = append(l.Sources, Source{Kind: SourceDropIn, Path: p})
	}
	merged, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	l.Merged = merged
	d, err := Parse(merged)
	if err != nil {
		return nil, fmt.Errorf("oauth provider %q (%s): %w", name, describeSources(l.Sources), err)
	}
	l.Descriptor = d
	if l.Sources[0].Kind == SourceOverride && isShipped {
		switch {
		case d.Base == "":
			l.Warnings = append(l.Warnings, fmt.Sprintf("oauth provider %q: %s replaces the descriptor built into hsh and records no \"base\"; it will not notice updates (consider a drop-in in %s instead)", name, overridePath, DropInDir(dir, name)))
		case d.Base != Hash(shippedData):
			l.Warnings = append(l.Warnings, fmt.Sprintf("oauth provider %q: the descriptor built into hsh changed since %s was copied from it; compare with hsh oauth provider cat %s --shipped", name, overridePath, name))
		}
	}
	return l, nil
}

func describeSources(srcs []Source) string {
	parts := make([]string, 0, len(srcs))
	for _, s := range srcs {
		parts = append(parts, s.Path)
	}
	return strings.Join(parts, " + ")
}

func Parse(data []byte) (Descriptor, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return Descriptor{}, err
	}
	switch {
	case probe.Version == 0:
		return Descriptor{}, fmt.Errorf("no \"version\" field (this hsh reads version %d)", FormatVersion)
	case probe.Version > FormatVersion:
		return Descriptor{}, fmt.Errorf("descriptor version %d is newer than this hsh reads (version %d); update hsh (hsh update)", probe.Version, FormatVersion)
	case probe.Version < FormatVersion:
		return Descriptor{}, fmt.Errorf("descriptor version %d is no longer supported (this hsh reads version %d)", probe.Version, FormatVersion)
	}
	var d Descriptor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Descriptor{}, err
	}
	return d, d.Validate()
}

func (d Descriptor) Validate() error {
	if d.TokenEndpoint.Host == "" || d.TokenEndpoint.Path == "" {
		return fmt.Errorf("token_endpoint needs a host and a path")
	}
	if !strings.HasPrefix(d.TokenEndpoint.Path, "/") {
		return fmt.Errorf("token_endpoint.path %q must start with /", d.TokenEndpoint.Path)
	}
	if len(d.ResponseFields) == 0 {
		return fmt.Errorf("response_fields is empty")
	}
	for _, f := range d.ResponseFields {
		if f.Path == "" {
			return fmt.Errorf("a response_fields entry has no path")
		}
		switch f.Kind {
		case "", "opaque", "jwt":
		default:
			return fmt.Errorf("response field %q: unknown kind %q (want opaque or jwt)", f.Path, f.Kind)
		}
		if f.Format != "" && strings.Count(f.Format, PhantomPlaceholder) != 1 {
			return fmt.Errorf("response field %q: format %q must contain %s exactly once", f.Path, f.Format, PhantomPlaceholder)
		}
		if f.Format != "" && f.Kind == "jwt" {
			return fmt.Errorf("response field %q: format and kind jwt cannot be combined", f.Path)
		}
	}
	if len(d.API.Hosts) == 0 {
		return fmt.Errorf("api.hosts is empty")
	}
	if d.Refresh != nil {
		if err := d.Refresh.validate(); err != nil {
			return err
		}
	}
	for name, seed := range d.Seeds {
		if err := seed.validate(name); err != nil {
			return err
		}
	}
	return nil
}

func (d Descriptor) Resolve(name string, narrow []string) (Resolved, error) {
	hosts := d.API.Hosts
	if narrow != nil {
		allowed := map[string]bool{}
		for _, h := range d.API.Hosts {
			allowed[h] = true
		}
		for _, h := range narrow {
			if !allowed[h] {
				return Resolved{}, fmt.Errorf("oauth provider %q: api host %q is not one of the provider's api hosts %v (a config may narrow them, never widen them)", name, h, d.API.Hosts)
			}
		}
		hosts = narrow
	}
	header := d.API.Header
	if header == "" {
		header = "Authorization"
	}
	prefix := ""
	if d.API.Prefix != nil {
		prefix = *d.API.Prefix
	} else if strings.EqualFold(header, "Authorization") {
		prefix = "Bearer "
	}
	return Resolved{
		Name:               name,
		TokenEndpoint:      d.TokenEndpoint,
		ResponseFields:     d.ResponseFields,
		RequestNonceFields: d.RequestNonceFields,
		APIHosts:           append([]string(nil), hosts...),
		Header:             header,
		Prefix:             prefix,
		ExpiresIn:          d.ExpiresIn,
	}, nil
}

func MergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	} else {
		cp := make(map[string]any, len(tm))
		for k, v := range tm {
			cp[k] = v
		}
		tm = cp
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = MergePatch(tm[k], v)
	}
	return tm
}
