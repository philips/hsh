package config

import (
	"strings"
	"testing"
)

func TestParseTasks(t *testing.T) {
	data := []byte("\n# a comment\nInstall gh and clone https://github.com/philips/hsh\n\n   \n# another\nSetup a Golang environment\n")
	tasks := ParseTasks(data)
	want := []string{"Install gh and clone https://github.com/philips/hsh", "Setup a Golang environment"}
	if len(tasks) != len(want) {
		t.Fatalf("got %d tasks, want %d: %v", len(tasks), len(want), tasks)
	}
	for i := range want {
		if tasks[i] != want[i] {
			t.Errorf("task %d: got %q, want %q", i, tasks[i], want[i])
		}
	}
	if empty := ParseTasks([]byte("# only comments\n\n")); len(empty) != 0 {
		t.Errorf("expected no tasks, got %v", empty)
	}
}

func TestComposePrompt(t *testing.T) {
	prompt := ComposePrompt([]string{"first task", "second task"})
	want := "Complete the following tasks:\n\n1. first task\n2. second task\n"
	if prompt != want {
		t.Fatalf("got %q, want %q", prompt, want)
	}
	if !strings.HasPrefix(prompt, "Complete the following tasks:") {
		t.Fatal("prompt must start with the instruction")
	}
}
