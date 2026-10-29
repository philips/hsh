package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadVersionAndStrictDecoding(t *testing.T) {
	future := Version + 1
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"current", `{"version":1,"network":{"mode":"allowlist"}}`, ""},
		{"missing", `{"network":{"mode":"allowlist"}}`, `no "version" field (this hsh reads version 1)`},
		{"newer", fmt.Sprintf(`{"version":%d}`, future), fmt.Sprintf("config version %d is newer than this hsh reads (version 1); update hsh (hsh update)", future)},
		{"older", `{"version":0}`, "config version 0 is no longer supported (this hsh reads version 1); regenerate it"},
		{"string version", `{"version":"1"}`, `"version" must be an integer, got "1"`},
		{"float version", `{"version":1.5}`, `"version" must be an integer, got 1.5`},
		{"unknown top-level", `{"version":1,"allw":["a"]}`, `unknown field "allw"`},
		{"unknown nested", `{"version":1,"network":{"approval_wiat":"30s"}}`, `unknown field "approval_wiat"`},
		{"unknown in map value", `{"version":1,"secrets":{"K":{"from":"env://X","hostz":[]}}}`, `unknown field "hostz"`},
		{"wrong type", `{"version":1,"network":{"allow":"a.example"}}`, "cannot unmarshal string"},
		{"trailing data", `{"version":1}{"version":1}`, "invalid character '{' after top-level value"},
		{"not json", `version: 1`, "invalid character"},
		{"invalid mode still validated", `{"version":1,"network":{"mode":"open"}}`, `invalid network mode "open"`},
	} {
		t.Run("file/"+tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			checkErr(t, err, tc.want, path)
			if tc.want == "" && cfg.Version != Version {
				t.Fatalf("version = %d", cfg.Version)
			}
		})
		t.Run("label/"+tc.name, func(t *testing.T) {
			_, err := FromImageLabel(tc.body)
			want := tc.want
			if tc.name == "older" || tc.name == "missing" {
				want += "; rebuild the image"
			}
			checkErr(t, err, want, "org.ifup.hsh image label")
		})
	}
}

func checkErr(t *testing.T, err error, want, source string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	if !strings.HasPrefix(err.Error(), source+": ") {
		t.Fatalf("error %q does not start with the source %q", err, source)
	}
}

func TestVersionErrorIsTyped(t *testing.T) {
	_, err := FromImageLabel(fmt.Sprintf(`{"version":%d}`, Version+1))
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Kind != VersionNewer || ve.Stale() {
		t.Fatalf("newer: %v", err)
	}
	if strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("a newer label must say to update hsh, not to rebuild: %v", err)
	}
	_, err = FromImageLabel(`{"shell":"/bin/sh"}`)
	if !errors.As(err, &ve) || ve.Kind != VersionMissing || !ve.Stale() {
		t.Fatalf("missing: %v", err)
	}
}

func TestEmptyLabelIsTheDefault(t *testing.T) {
	cfg, err := FromImageLabel("")
	if err != nil || cfg.Version != Version {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestDefaultCarriesVersion(t *testing.T) {
	data, err := marshalForTest(Default())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("a marshalled config has no version: %s", data)
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("a marshalled default does not load back: %v", err)
	}
}

func TestEditorsRefuseUnreadableVersions(t *testing.T) {
	for name, body := range map[string]string{
		"newer":   fmt.Sprintf("{\n  \"version\": %d,\n  \"network\": {\"allow\": [\"a.example\"], \"futurefield\": 1}\n}\n", Version+1),
		"missing": "{\n  \"network\": {\"allow\": [\"a.example\"]}\n}\n",
	} {
		editors := map[string]func(path string) error{
			"AddAllowedHosts": func(p string) error { _, err := AddAllowedHosts(p, []string{"b.example"}); return err },
			"SetApprovalWait": func(p string) error { return SetApprovalWait(p, "30s") },
			"AddSecret":       func(p string) error { return AddSecret(p, "K", Secret{From: "env://K"}) },
		}
		for ename, edit := range editors {
			t.Run(name+"/"+ename, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "c.json")
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				var ve *VersionError
				if err := edit(path); !errors.As(err, &ve) {
					t.Fatalf("want a version error, got %v", err)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(after, []byte(body)) {
					t.Fatalf("the file was modified:\n%s", after)
				}
			})
		}
	}
}

func TestEditorsKeepUnknownFieldsOfAReadableVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	body := `{"version":1,"network":{"allow":["a.example"]},"extra":{"k":1}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAllowedHosts(path, []string{"b.example"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), `"extra"`) || !strings.Contains(string(after), "b.example") || !strings.Contains(string(after), `"version": 1`) {
		t.Fatalf("edit dropped fields: %s", after)
	}
}

func marshalForTest(c *Config) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}
