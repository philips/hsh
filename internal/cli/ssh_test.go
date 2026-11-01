package cli

import (
	"strings"
	"testing"
)

func TestSSHConfigStanzaLocal(t *testing.T) {
	got := sshConfigStanza("", "hsh")
	for _, want := range []string{"Host hsh-*\n", "ProxyCommand hsh ssh proxy %h\n", "StrictHostKeyChecking no\n", "UserKnownHostsFile /dev/null\n", "ForwardAgent no\n", "ForwardX11 no\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("stanza missing %q:\n%s", want, got)
		}
	}
}

func TestSSHConfigStanzaRemote(t *testing.T) {
	got := sshConfigStanza("me@server", "/home/me/bin/hsh")
	want := "ProxyCommand ssh -T -q me@server /home/me/bin/hsh ssh proxy %h\n"
	if !strings.Contains(got, want) {
		t.Fatalf("stanza missing %q:\n%s", want, got)
	}
}

func TestHasMount(t *testing.T) {
	inspect := "/hsh-workspace\n/hsh-workspace-inner\n/hsh-sshd\n/workspace\n"
	if !hasMount(inspect, sshdPath) {
		t.Fatal("did not find /hsh-sshd")
	}
	if hasMount("/hsh-workspace\n/hsh-workspace-inner\n/hsh-sshd-old\n", sshdPath) {
		t.Fatal("matched a different mount")
	}
	if hasMount("", sshdPath) {
		t.Fatal("matched an empty mount list")
	}
}

func TestLegacySessionError(t *testing.T) {
	got := legacySessionError("abc123").Error()
	for _, want := range []string{"session abc123 was created by an older hsh", "hsh rm abc123", "hsh run"} {
		if !strings.Contains(got, want) {
			t.Errorf("error missing %q: %s", want, got)
		}
	}
}
