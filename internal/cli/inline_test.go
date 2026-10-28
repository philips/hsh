package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

func TestSessionDenialsCountAndMonitorOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	dbPath := filepath.Join(dir, "denied.db")
	sessionDir := denieddb.SessionDir("s1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(sessionDir, "config.json")
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
	if n, held, err := src.Count(); err != nil || n != 2 || held != 0 {
		t.Fatalf("count: %d %d %v", n, held, err)
	}
	hdb, _ := denieddb.Open(dbPath)
	if err := denieddb.AdjustHolding(hdb, "s1", "a.example", 2); err != nil {
		t.Fatal(err)
	}
	hdb.Close()
	if n, held, _ := src.Count(); n != 2 || held != 1 {
		t.Fatalf("count with one held host: %d %d", n, held)
	}
	hdb, _ = denieddb.Open(dbPath)
	_ = denieddb.ResetHolding(hdb, "s1")
	hdb.Close()

	model := src.Overlay(100, 30)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := model.View()
	for _, want := range []string{"denied hosts awaiting review", "session s1", "https://a.example", "https://b.example"} {
		if !strings.Contains(view, want) {
			t.Errorf("the overlay is not the monitor view: missing %q in\n%s", want, view)
		}
	}
	if strings.Contains(view, "other.example") {
		t.Error("another session's denial is listed")
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	model, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("approving returned no command")
	}
	model, _ = model.Update(cmd())
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Network.Allow; len(got) != 2 || got[1] != "a.example" && got[1] != "b.example" {
		t.Fatalf("the approval did not reach the session config: %v", got)
	}
	if n, _, _ := src.Count(); n != 1 {
		t.Fatalf("count after approving one host: %d", n)
	}
	_ = model
}

func TestPastingIntoTheMonitorFilterDoesNotTriggerCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	dbPath := filepath.Join(dir, "denied.db")
	db, err := denieddb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"a.example", "b.example"} {
		if err := denieddb.Record(db, "/ws", "s1", h, h+":443"); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	src := &sessionDenials{dbPath: dbPath, session: "s1"}
	defer src.Close()

	model := src.Overlay(120, 30)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	paste := func(text string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true}
	}

	model, cmd := model.Update(paste("qaiio"))
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("pasted text outside the filter quit the monitor")
		}
	}
	if !strings.Contains(model.View(), "https://a.example") || !strings.Contains(model.View(), "https://b.example") {
		t.Fatal("pasted text outside the filter changed the list")
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model, _ = model.Update(paste("b.exa"))
	view := model.View()
	if !strings.Contains(view, "https://b.example") || strings.Contains(view, "https://a.example") {
		t.Fatalf("pasting into the filter did not filter the list:\n%s", view)
	}
}

func TestSessionDenialsWithoutADatabase(t *testing.T) {
	src := &sessionDenials{dbPath: filepath.Join(t.TempDir(), "none.db"), session: "s", cfgPath: "x"}
	n, held, err := src.Count()
	if err != nil || n != 0 || held != 0 {
		t.Fatalf("%v %v %v", n, held, err)
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

func TestCtrlCClosesTheMonitorOverlayInsteadOfInterruptingTheSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	src := &sessionDenials{dbPath: filepath.Join(dir, "denied.db"), session: "s1"}
	defer src.Close()
	model := src.Overlay(100, 30)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C did not quit the overlay model")
	}
}
