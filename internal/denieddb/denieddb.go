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
	allowed_at TEXT
);
CREATE TABLE IF NOT EXISTS dns_denied (
	host       TEXT PRIMARY KEY,
	target     TEXT NOT NULL,
	count      INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL,
	allowed    INTEGER NOT NULL DEFAULT 0,
	allowed_at TEXT
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
}

func DefaultPath() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "harnessh", "denied.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "denied.db"
	}
	return filepath.Join(home, ".local", "state", "harnessh", "denied.db")
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
	return db, nil
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
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at
		FROM denied WHERE allowed = 0 ORDER BY count DESC, host ASC`)
}

func All(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at
		FROM denied ORDER BY allowed ASC, count DESC, host ASC`)
}

func PendingDNS(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at
		FROM dns_denied WHERE allowed = 0 ORDER BY count DESC, host ASC`)
}

func AllDNS(db *sql.DB) ([]DeniedHost, error) {
	return query(db, `SELECT host, target, count, first_seen, last_seen, allowed, allowed_at
		FROM dns_denied ORDER BY allowed ASC, count DESC, host ASC`)
}

func MarkAllowed(db *sql.DB, hosts []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, host := range hosts {
		for _, table := range []string{"denied", "dns_denied"} {
			if _, err := db.Exec(`UPDATE `+table+` SET allowed = 1, allowed_at = ? WHERE host = ?`, now, host); err != nil {
				return err
			}
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
		var allowed int
		var allowedAt sql.NullString
		if err := rows.Scan(&h.Host, &h.Target, &h.Count, &h.FirstSeen, &h.LastSeen, &allowed, &allowedAt); err != nil {
			return nil, err
		}
		h.Allowed = allowed != 0
		h.AllowedAt = allowedAt.String
		out = append(out, h)
	}
	return out, rows.Err()
}
