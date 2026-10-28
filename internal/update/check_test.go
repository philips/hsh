package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "metrics.db")
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.9", "v0.2.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0", "v0.1.9", false},
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v0.9.0", false},
		{"v1.2.3", "v1.3.0", true},
		{"v1.2.3", "v1.2.3-rc1", false},
		{"v1.2.3", "banana", false},
		{"v1.2.3", "v1.2", false},
		{"v1.2.3", "v1.2.3.4", false},
		{"v1.2.3", "", false},
		{"", "v0.2.0", true},
		{"dev", "v0.2.0", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestCheckStateRoundTrip(t *testing.T) {
	db := testDB(t)
	if CheckDue(db, time.Hour) {
		t.Error("a missing database is not due (nothing enabled it)")
	}
	if AutoEnabled(db) {
		t.Error("autoupdate enabled without a database")
	}
	if err := SetAuto(db, true); err != nil {
		t.Fatal(err)
	}
	if !AutoEnabled(db) {
		t.Error("SetAuto(true) did not enable autoupdate")
	}
	if !CheckDue(db, time.Hour) {
		t.Error("no last check recorded, so a check is due")
	}
	if err := MarkCheck(db); err != nil {
		t.Fatal(err)
	}
	if CheckDue(db, time.Hour) {
		t.Error("a fresh check is not due again")
	}
	if !CheckDue(db, 0) {
		t.Error("a zero interval should consider even a fresh check due")
	}
	if err := SetLatest(db, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if got := KnownLatest(db); got != "v1.2.3" {
		t.Errorf("KnownLatest = %q, want v1.2.3", got)
	}
	if err := ClearLatest(db); err != nil {
		t.Fatal(err)
	}
	if got := KnownLatest(db); got != "" {
		t.Errorf("KnownLatest after clear = %q, want empty", got)
	}
	if err := SetAuto(db, false); err != nil {
		t.Fatal(err)
	}
	if AutoEnabled(db) {
		t.Error("SetAuto(false) did not disable autoupdate")
	}
}

func TestCheckStateCorruptDB(t *testing.T) {
	db := testDB(t)
	if err := os.WriteFile(db, []byte("not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if AutoEnabled(db) {
		t.Error("autoupdate enabled on a corrupt database")
	}
	if KnownLatest(db) != "" {
		t.Error("KnownLatest on a corrupt database should be empty")
	}
	if CheckDue(db, time.Hour) {
		t.Error("a corrupt database should not be considered due")
	}
	if err := SetAuto(db, true); err == nil {
		t.Error("SetAuto on a corrupt database should fail")
	}
}
