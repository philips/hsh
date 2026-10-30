package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"hsh/internal/config"
)

func TestSessionConfigErrorHints(t *testing.T) {
	_, err := config.FromImageLabel(fmt.Sprintf(`{"version":%d}`, config.Version+1))
	got := sessionConfigError("abc123", err).Error()
	if !strings.Contains(got, "session abc123: ") || !strings.Contains(got, "update hsh") || strings.Contains(got, "hsh rm") {
		t.Fatalf("newer: %q", got)
	}
	_, err = config.FromImageLabel(`{"shell":"/bin/sh"}`)
	got = sessionConfigError("abc123", err).Error()
	if !strings.Contains(got, "remove the session with: hsh rm abc123") {
		t.Fatalf("missing: %q", got)
	}
	got = sessionConfigError("abc123", errors.New("boom")).Error()
	if got != "session abc123: boom (remove orphaned session state with: hsh rm --garbage)" {
		t.Fatalf("other: %q", got)
	}
}
