package cli

import (
	"reflect"
	"testing"
)

func TestTerminalEnvArgs(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	got := terminalEnvArgs(env(map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.5.0", "TERM": "xterm-256color", "SECRET": "x"}))
	want := []string{"-e", "TERM_PROGRAM=iTerm.app", "-e", "TERM_PROGRAM_VERSION=3.5.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	got = terminalEnvArgs(env(map[string]string{"TERM": "xterm-ghostty"}))
	if !reflect.DeepEqual(got, []string{"-e", "FORCE_HYPERLINK=1"}) {
		t.Fatalf("a hyperlink-capable TERM must set FORCE_HYPERLINK inside, where TERM is xterm: %v", got)
	}
	got = terminalEnvArgs(env(map[string]string{"TERM": "xterm-kitty", "FORCE_HYPERLINK": "0"}))
	if !reflect.DeepEqual(got, []string{"-e", "FORCE_HYPERLINK=0"}) {
		t.Fatalf("an explicit FORCE_HYPERLINK wins: %v", got)
	}
	got = terminalEnvArgs(env(map[string]string{"TERM": "tmux-256color", "TERM_PROGRAM": "tmux", "TERM_PROGRAM_VERSION": "3.4"}))
	if !reflect.DeepEqual(got, []string{"-e", "TERM_PROGRAM=tmux", "-e", "TERM_PROGRAM_VERSION=3.4", "-e", "FORCE_HYPERLINK=1"}) {
		t.Fatalf("tmux forwards OSC 8 to the outer terminal, so a session under tmux must set FORCE_HYPERLINK: %v", got)
	}
	if got := terminalEnvArgs(env(map[string]string{"TERM": "screen-256color"})); len(got) != 0 {
		t.Fatalf("screen does not forward OSC 8, so it must not set FORCE_HYPERLINK: %v", got)
	}
	if got := terminalEnvArgs(env(map[string]string{"TERM": "xterm"})); len(got) != 0 {
		t.Fatalf("a plain xterm forwards nothing: %v", got)
	}
}
