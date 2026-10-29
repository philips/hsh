package cli

import (
	"strings"
	"testing"
)

func TestSSHConfigStanzaLocal(t *testing.T) {
	got := sshConfigStanza("", "hsh")
	for _, want := range []string{"Host hsh-*\n", "ProxyCommand hsh ssh-proxy %h\n", "StrictHostKeyChecking no\n", "UserKnownHostsFile /dev/null\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("stanza missing %q:\n%s", want, got)
		}
	}
}

func TestSSHConfigStanzaRemote(t *testing.T) {
	got := sshConfigStanza("me@server", "/home/me/bin/hsh")
	want := "ProxyCommand ssh -T -q me@server /home/me/bin/hsh ssh-proxy %h\n"
	if !strings.Contains(got, want) {
		t.Fatalf("stanza missing %q:\n%s", want, got)
	}
}
