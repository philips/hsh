package imagetrust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustIsRecordedPerImageID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hsh", "trusted-images")
	id := "0123456789abcdef0123456789abcdef"
	if ok, err := Trusted(path, id); err != nil || ok {
		t.Fatalf("trusted before trusting: %v %v", ok, err)
	}
	if err := Trust(path, id, "ghcr.io/philips/hsh-pi:latest"); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path, id, "again"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Trusted(path, id); !ok {
		t.Fatal("not trusted after trusting")
	}
	if ok, _ := Trusted(path, "fedcba9876543210fedcba9876543210"); ok {
		t.Fatal("another image id is trusted")
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), id) != 1 || !strings.Contains(string(data), "ghcr.io/philips/hsh-pi:latest") {
		t.Fatalf("file = %q", data)
	}
	for _, bad := range []string{"", "#", "short", "0123456789ABCDEF", "../../etc/passwd"} {
		if err := Trust(path, bad, "x"); err == nil {
			t.Errorf("trusted the id %q", bad)
		}
		if ok, _ := Trusted(path, bad); ok {
			t.Errorf("%q is trusted", bad)
		}
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
