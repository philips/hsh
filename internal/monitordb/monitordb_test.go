package monitordb

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMarkIgnored(t *testing.T) {
	db := openTemp(t)
	defer db.Close()
	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIgnored(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	pending, err := Pending(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("ignored host still pending: %+v", pending)
	}
	ignored, err := Ignored(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 {
		t.Fatalf("expected 1 ignored host, got %d", len(ignored))
	}
	if ignored[0].Host != "a.example.com" || !ignored[0].Ignored || !ignored[0].Allowed {
		t.Errorf("unexpected ignored row: %+v", ignored[0])
	}

	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	pending, err = Pending(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("a later denial brought an ignored host back to the review list: %+v", pending)
	}
}

func TestMarkAllowedClearsIgnored(t *testing.T) {
	db := openTemp(t)
	defer db.Close()
	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIgnored(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkAllowed(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	ignored, err := Ignored(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 {
		t.Errorf("approved host still listed as ignored: %+v", ignored)
	}
	all, err := All(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Allowed || all[0].Ignored {
		t.Errorf("unexpected row after approve: %+v", all)
	}
}

func TestLookupDecision(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if d, err := LookupDecision(db, "s1", "a.example"); err != nil || d.Found {
		t.Fatalf("unknown host: %+v %v", d, err)
	}
	if err := Record(db, "/ws", "s1", "a.example", "a.example:443"); err != nil {
		t.Fatal(err)
	}
	if d, _ := LookupDecision(db, "s1", "a.example"); !d.Found || d.Allowed || d.Ignored {
		t.Fatalf("pending: %+v", d)
	}
	if err := MarkIgnored(db, "s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := LookupDecision(db, "s1", "a.example"); !d.Ignored || !d.Allowed {
		t.Fatalf("ignored: %+v", d)
	}
	if d, _ := LookupDecision(db, "s2", "a.example"); d.Found {
		t.Fatalf("another session's row leaked: %+v", d)
	}
	if err := MarkAllowed(db, "s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := LookupDecision(db, "s1", "a.example"); !d.Allowed || d.Ignored {
		t.Fatalf("approved: %+v", d)
	}
}

func TestHoldingCounts(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, h := range []string{"a.example", "b.example", "b.example", "c.example"} {
		if err := Record(db, "/ws", "s1", h, h+":443"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Record(db, "/ws", "s2", "a.example", "x"); err != nil {
		t.Fatal(err)
	}
	holding := func(session string) map[string]int {
		rows, err := Pending(db, Filter{Session: session})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, r := range rows {
			out[r.Host] = r.Holding
		}
		return out
	}
	if err := AdjustHolding(db, "s1", "a.example", 1); err != nil {
		t.Fatal(err)
	}
	if err := AdjustHolding(db, "s1", "a.example", 1); err != nil {
		t.Fatal(err)
	}
	if got := holding("s1"); got["a.example"] != 2 || got["b.example"] != 0 {
		t.Fatalf("after two holds: %v", got)
	}
	rows, _ := Pending(db, Filter{Session: "s1"})
	if rows[0].Host != "a.example" {
		t.Fatalf("held hosts must be listed first, got %s (count order would put b.example first)", rows[0].Host)
	}
	if err := AdjustHolding(db, "s1", "a.example", -1); err != nil {
		t.Fatal(err)
	}
	if err := AdjustHolding(db, "s1", "c.example", -5); err != nil {
		t.Fatal(err)
	}
	if got := holding("s1"); got["a.example"] != 1 || got["c.example"] != 0 {
		t.Fatalf("decrements: %v", got)
	}
	if err := AdjustHolding(db, "s2", "a.example", 3); err != nil {
		t.Fatal(err)
	}
	if err := ResetHolding(db, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := holding("s1"); got["a.example"] != 0 {
		t.Fatalf("reset: %v", got)
	}
	if got := holding("s2"); got["a.example"] != 3 {
		t.Fatalf("another session's counts were reset: %v", got)
	}
}
