package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/config"
	"hsh/internal/secretstore"
)

func TestPromptSecretHostsRecordsOnlyApprovedHosts(t *testing.T) {
	store, _, err := secretstore.Open(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Set("key", "v", false); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("bound", "w", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHosts("bound", []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Secrets: map[string]config.Secret{
		"A": {From: "hsh://key", Hosts: []string{"api.example.com", "evil.example"}},
		"B": {From: "hsh://bound", Hosts: []string{"api.example.com"}},
		"C": {From: "hsh://missing", Hosts: []string{"x.example"}},
	}}
	var out bytes.Buffer
	if err := promptSecretHosts(cfg, store, "the org.ifup.hsh label of img", strings.NewReader("y\nn\n"), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"hsh://key has no approved hosts yet",
		"the org.ifup.hsh label of img asks to send hsh://key (as A) to api.example.com. Allow? [y/N]",
		"hsh://key may now be sent to api.example.com",
		"asks to send hsh://key (as A) to evil.example",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hsh://bound") || strings.Contains(text, "hsh://missing") {
		t.Errorf("asked about an approved or missing secret:\n%s", text)
	}
	if hosts, _ := store.SecretHosts("key"); strings.Join(hosts, ",") != "api.example.com" {
		t.Fatalf("hosts = %v", hosts)
	}

	out.Reset()
	if err := promptSecretHosts(cfg, store, "src", strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := store.SecretHosts("key"); strings.Join(hosts, ",") != "api.example.com" {
		t.Fatalf("end of input approved a host: %v", hosts)
	}
	if strings.Contains(out.String(), "no approved hosts yet") {
		t.Errorf("explained again for a secret that has hosts:\n%s", out.String())
	}

	res, err := config.ResolveSecrets(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets.Secrets) != 2 || len(res.Unapproved) != 1 {
		t.Fatalf("%+v", res)
	}
	w := unapprovedWarning(res.Unapproved[0], "config file /x.hsh")
	if !strings.Contains(w, "secret A (hsh://key) is not sent to evil.example: config file /x.hsh asks for it") || !strings.HasSuffix(w, "hsh secret allow key evil.example") {
		t.Fatalf("warning = %q", w)
	}
}
