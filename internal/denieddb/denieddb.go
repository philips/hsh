package denieddb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS denied (
	host       TEXT PRIMARY KEY,
	target     TEXT NOT NULL,
	count      INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL,
	allowed    INTEGER NOT NULL DEFAULT 0,
	allowed_at TEXT,
	ignored    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS dns_denied (
	host       TEXT PRIMARY KEY,
	target     TEXT NOT NULL,
	count      INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL,
	allowed    INTEGER NOT NULL DEFAULT 0,
	allowed_at TEXT,
	ignored    INTEGER NOT NULL DEFAULT 0
);
`

type DeniedHost struct {
	Host      string
	Target    string
	Count     int
	FirstSeen string
	LastSeen  string
	Allowed   bool
	AllowedAt string
	Ignored   bool
}

func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "harnessh")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "harnessh-state"
	}
	return filepath.Join(home, ".local", "state", "harnessh")
}

func DefaultPath() string {
	return filepath.Join(StateDir(), "denied.db")
}

func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize denial database: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate denial database: %w", err)
	}
	return db, nil
}

// migrate upgrades databases created before a column was added.
func migrate(db *sql.DB) error {
	for _, table := range []string{"denied", "dns_denied"} {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var cid int
			var name, ctype string
			var notNull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		hasIgnored := false
		for _, c := range columns {
			if c == "ignored" {
				hasIgnored = true
				break
			}
		}
		if !hasIgnored {
			if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN ignored INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	return nil
}

func OpenReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	return db, nil
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Record(db *sql.DB, host, target string) error {
	return record(db, "denied", host, target)
}

func RecordDNS(db *sql.DB, host, target string) error {
	return record(db, "dns_denied", host, target)
}

func record(db *sql.DB, table, host, target string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO `+table+` (host, target, count, first_seen, last_seen, allowed)
		 VALUES (?, ?, 1, ?, ?, 0)
		 ON CONFLICT(host) DO UPDATE SET
		 	count = `+table+`.count + 1,
		 	last_seen = excluded.last_seen`,
		host, target, now, now,
	)
	return err
}

func Pending(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at, ignored
		FROM denied WHERE allowed = 0 ORDER BY count DESC, host ASC`)
}

func All(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at, ignored
		FROM denied ORDER BY allowed ASC, count DESC, host ASC`)
}

func Ignored(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at, ignored
		FROM denied WHERE ignored = 1 ORDER BY last_seen DESC, host ASC`)
}

func PendingDNS(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at, ignored
		FROM dns_denied WHERE allowed = 0 ORDER BY count DESC, host ASC`)
}

func AllDNS(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at, ignored
		FROM dns_denied ORDER BY allowed ASC, count DESC, host ASC`)
}

func MarkAllowed(db *sql.DB, hosts []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, host := range hosts {
		for _, table := range []string{"denied", "dns_denied"} {
			if _, err := db.Exec(`UPDATE `+table+` SET allowed = 1, ignored = 0, allowed_at = ? WHERE host = ?`, now, host); err != nil {
				return err
			}
		}
	}
	return nil
}

// MarkIgnored marks hosts as handled without allowing them: they leave the
// review list permanently (a later denial does not reset allowed).
func MarkIgnored(db *sql.DB, hosts []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, host := range hosts {
		if _, err := db.Exec(`UPDATE denied SET allowed = 1, ignored = 1, allowed_at = ? WHERE host = ?`, now, host); err != nil {
			return err
		}
	}
	return nil
}

func query(db *sql.DB, q string) ([]DeniedHost, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeniedHost
	for rows.Next() {
		var h DeniedHost
		var allowed, ignored int
		var allowedAt sql.NullString
		if err := rows.Scan(&h.Host, &h.Target, &h.Count, &h.FirstSeen, &h.LastSeen, &allowed, &allowedAt, &ignored); err != nil {
			return nil, err
		}
		h.Allowed = allowed != 0
		h.AllowedAt = allowedAt.String
		h.Ignored = ignored != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
