package monitordb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"hsh/internal/dbschema"
	"hsh/internal/state"
)

const SchemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS denied (
	host       TEXT NOT NULL,
	session    TEXT NOT NULL DEFAULT '',
	workspace  TEXT NOT NULL DEFAULT '',
	target     TEXT NOT NULL,
	count      INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL,
	allowed    INTEGER NOT NULL DEFAULT 0,
	allowed_at TEXT,
	ignored    INTEGER NOT NULL DEFAULT 0,
	holding    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (host, session)
);
`

type DeniedHost struct {
	Host      string
	Session   string
	Workspace string
	Target    string
	Count     int
	FirstSeen string
	LastSeen  string
	Allowed   bool
	AllowedAt string
	Ignored   bool
	Holding   int
}

type Filter struct {
	Session   string
	Workspace string
}

func (f Filter) where(extra ...string) (string, []any) {
	conds := append([]string{}, extra...)
	var args []any
	if f.Session != "" {
		conds = append(conds, "session = ?")
		args = append(args, f.Session)
	}
	if f.Workspace != "" {
		conds = append(conds, "workspace = ?")
		args = append(args, f.Workspace)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func DefaultPath() string {
	return filepath.Join(state.Dir(), "monitor.db")
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
	if err := dbschema.Apply(db, "monitor.db", path, SchemaVersion, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("monitor database: %w", err)
	}
	return db, nil
}

func AdjustHolding(db *sql.DB, session, host string, delta int) error {
	_, err := db.Exec(`UPDATE denied SET holding = MAX(0, holding + ?) WHERE host = ? AND session = ?`, delta, host, session)
	return err
}

func ResetHolding(db *sql.DB, session string) error {
	_, err := db.Exec(`UPDATE denied SET holding = 0 WHERE session = ?`, session)
	return err
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Record(db *sql.DB, workspace, session, host, target string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO denied (host, session, workspace, target, count, first_seen, last_seen, allowed)
		 VALUES (?, ?, ?, ?, 1, ?, ?, 0)
		 ON CONFLICT(host, session) DO UPDATE SET
		 	count = denied.count + 1,
		 	last_seen = excluded.last_seen`,
		host, session, workspace, target, now, now,
	)
	return err
}

const deniedCols = "host, session, workspace, target, count, first_seen, last_seen, allowed, allowed_at, ignored, holding"

func Pending(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where("allowed = 0")
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY (holding > 0) DESC, count DESC, host ASC`, args...)
}

func All(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where()
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY allowed ASC, count DESC, host ASC`, args...)
}

func Ignored(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where("ignored = 1")
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY last_seen DESC, host ASC`, args...)
}

type Decision struct {
	Found   bool
	Allowed bool
	Ignored bool
}

func LookupDecision(db *sql.DB, session, host string) (Decision, error) {
	var allowed, ignored int
	err := db.QueryRow(`SELECT allowed, ignored FROM denied WHERE host = ? AND session = ?`, host, session).Scan(&allowed, &ignored)
	if err == sql.ErrNoRows {
		return Decision{}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	return Decision{Found: true, Allowed: allowed != 0, Ignored: ignored != 0}, nil
}

func MarkAllowed(db *sql.DB, session string, hosts []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, host := range hosts {
		if _, err := db.Exec(`UPDATE denied SET allowed = 1, ignored = 0, allowed_at = ? WHERE host = ? AND session = ?`, now, host, session); err != nil {
			return err
		}
	}
	return nil
}

func MarkIgnored(db *sql.DB, session string, hosts []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, host := range hosts {
		if _, err := db.Exec(`UPDATE denied SET allowed = 1, ignored = 1, allowed_at = ? WHERE host = ? AND session = ?`, now, host, session); err != nil {
			return err
		}
	}
	return nil
}

func query(db *sql.DB, q string, args ...any) ([]DeniedHost, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeniedHost
	for rows.Next() {
		var h DeniedHost
		var allowed, ignored int
		var allowedAt sql.NullString
		if err := rows.Scan(&h.Host, &h.Session, &h.Workspace, &h.Target, &h.Count, &h.FirstSeen, &h.LastSeen, &allowed, &allowedAt, &ignored, &h.Holding); err != nil {
			return nil, err
		}
		h.Allowed = allowed != 0
		h.AllowedAt = allowedAt.String
		h.Ignored = ignored != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
