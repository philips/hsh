package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPodmanOutputIsStdoutOnlyOnSuccess(t *testing.T) {
	out, err := runPodman(exec.Command("sh", "-c", `echo '{"Name":"hsh-a"}'; echo 'level=error msg="OCI Runtime ... is in use"' >&2`), []string{"pod", "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "{\"Name\":\"hsh-a\"}\n" {
		t.Fatalf("stderr leaked into the parsed output: %q", out)
	}
	out, err = runPodman(exec.Command("sh", "-c", `echo partial; echo 'Error: no such pod' >&2; exit 125`), []string{"pod", "rm", "x"})
	if err == nil || !strings.Contains(err.Error(), "podman pod rm x") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "Error: no such pod") || !strings.Contains(out, "partial") {
		t.Fatalf("a failure lost its messages: %q", out)
	}
}
