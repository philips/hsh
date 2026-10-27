package imageRef

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsTransport(t *testing.T) {
	transport := []string{
		"docker://docker.io/library/busybox:latest",
		"docker://ghcr.io/philips/hsh-pi",
		"oci://layout:tag",
		"https://example.com/x",
		"docker-daemon://image",
	}
	for _, ref := range transport {
		if !IsTransport(ref) {
			t.Errorf("IsTransport(%q) = false, want true", ref)
		}
	}
	notTransport := []string{
		"pi",
		"pi:latest",
		"ghcr.io/philips/hsh-pi",
		"localhost/hsh-example:latest",
		"registry:5000/team/pi",
		"docker-archive:/tmp/img.tar",
		"dir:/path",
	}
	for _, ref := range notTransport {
		if IsTransport(ref) {
			t.Errorf("IsTransport(%q) = true, want false", ref)
		}
	}
}

func TestIsQualified(t *testing.T) {
	qualified := []string{
		"ghcr.io/philips/hsh-pi",
		"docker.io/library/busybox:latest",
		"localhost/hsh-example:latest",
		"localhost:5000/foo/bar",
		"registry.example.com:5000/team/pi",
		"docker-archive:/tmp/img.tar",
		"dir:/path/to/image",
		"oci:/layout:tag",
		"containers-storage:localhost/foo",
	}
	for _, ref := range qualified {
		if !IsQualified(ref) {
			t.Errorf("IsQualified(%q) = false, want true", ref)
		}
	}
	unqualified := []string{
		"pi",
		"pi:latest",
		"my-dev-image",
		"vibe:1.0",
	}
	for _, ref := range unqualified {
		if IsQualified(ref) {
			t.Errorf("IsQualified(%q) = true, want false", ref)
		}
	}
}

func TestShortnamesDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nope"))
	sn, err := LoadShortnames(ShortnamesPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := sn.Resolve("pi"); got != "docker://ghcr.io/philips/hsh-pi" {
		t.Fatalf("Resolve(pi) = %q, want the builtin default", got)
	}
	if got := sn.Resolve("pi:latest"); got != "docker://ghcr.io/philips/hsh-pi:latest" {
		t.Fatalf("Resolve(pi:latest) = %q", got)
	}
}

func TestShortnamesConfFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shortnames.conf")
	body := `# corporate override
shortname-prefix=docker://quay.io/example-corp-agents/

pi=docker://ghcr.io/philips/hsh-pi
mydev=docker://registry.example.com/team/pi:dev
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sn, err := LoadShortnames(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := sn.Resolve("pi"); got != "docker://ghcr.io/philips/hsh-pi" {
		t.Fatalf("explicit alias must win: %q", got)
	}
	if got := sn.Resolve("mydev"); got != "docker://registry.example.com/team/pi:dev" {
		t.Fatalf("alias value: %q", got)
	}
	if got := sn.Resolve("anything"); got != "docker://quay.io/example-corp-agents/anything" {
		t.Fatalf("prefix override: %q", got)
	}
	if got := sn.Resolve("agent:1.2"); got != "docker://quay.io/example-corp-agents/agent:1.2" {
		t.Fatalf("prefix with tag: %q", got)
	}
}

func TestShortnamesMissingFileIsDefaults(t *testing.T) {
	sn, err := LoadShortnames(filepath.Join(t.TempDir(), "missing.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if sn.Prefix != DefaultPrefix || len(sn.Aliases) != 0 {
		t.Fatal("missing file must leave the builtin defaults")
	}
}

func TestShortnamesMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shortnames.conf")
	if err := os.WriteFile(path, []byte("just a line without equals"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadShortnames(path); err == nil {
		t.Fatal("expected an error for a malformed line")
	}
}
