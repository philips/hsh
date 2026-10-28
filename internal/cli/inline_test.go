package cli

import (
	"os"
	"path/filepath"
	"testing"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

func TestSessionDenialsPendingAllowAndDeny(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "denied.db")
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"image":"x","network":{"mode":"allowlist","allow":["keep.example"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := denieddb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"a.example", "a.example", "b.example"} {
		if err := denieddb.Record(db, "/ws", "s1", h, h+":443"); err != nil {
			t.Fatal(err)
		}
	}
	if err := denieddb.Record(db, "/ws", "other", "other.example", "x:443"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	src := &sessionDenials{dbPath: dbPath, session: "s1", cfgPath: cfgPath}
	defer src.Close()
	items, err := src.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Host != "a.example" || items[0].Count != 2 || items[0].Last.IsZero() {
		t.Fatalf("pending: %+v", items)
	}

	if err := src.Allow("a.example"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Network.Allow; len(got) != 2 || got[0] != "keep.example" || got[1] != "a.example" {
		t.Fatalf("allow list: %v", got)
	}
	if err := src.Deny("b.example"); err != nil {
		t.Fatal(err)
	}
	items, _ = src.Pending()
	if len(items) != 0 {
		t.Fatalf("still pending after decisions: %+v", items)
	}
	other := &sessionDenials{dbPath: dbPath, session: "other", cfgPath: cfgPath}
	defer other.Close()
	if items, _ := other.Pending(); len(items) != 1 {
		t.Fatalf("another session's denial was affected: %+v", items)
	}
}

func TestSessionDenialsWithoutADatabase(t *testing.T) {
	src := &sessionDenials{dbPath: filepath.Join(t.TempDir(), "none.db"), session: "s", cfgPath: "x"}
	items, err := src.Pending()
	if err != nil || len(items) != 0 {
		t.Fatalf("%v %v", items, err)
	}
	if err := src.Allow("a.example"); err == nil {
		t.Fatal("allow without a database succeeded")
	}
}

func TestInlineMonitorIsOffWhenDisabledOrNotInteractive(t *testing.T) {
	cfg := config.Default()
	if im, _ := newInlineMonitor(cfg, "s", "db", "cfg", false); im != nil {
		t.Fatal("non-interactive session got a relay")
	}
	cfg.Monitor.Enabled = false
	if im, warn := newInlineMonitor(cfg, "s", "db", "cfg", true); im != nil || warn != "" {
		t.Fatalf("disabled monitor: %v %q", im, warn)
	}
}
