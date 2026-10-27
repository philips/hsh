package denieddb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

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

func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "hsh")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hsh-state"
	}
	return filepath.Join(home, ".local", "state", "hsh")
}

func SessionsDir() string {
	return filepath.Join(StateDir(), "sessions")
}

func SessionDir(session string) string {
	return filepath.Join(SessionsDir(), session)
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

func migrate(db *sql.DB) error {
	hasSession, hasWorkspace, hasIgnored, err := columns(db, "denied")
	if err != nil {
		return err
	}
	if !hasIgnored {
		if _, err := db.Exec("ALTER TABLE denied ADD COLUMN ignored INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if !hasSession || !hasWorkspace {
		if err := rebuild(db, "denied"); err != nil {
			return err
		}
	}
	if err := mergeLegacyDNS(db); err != nil {
		return err
	}
	return nil
}

func mergeLegacyDNS(db *sql.DB) error {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'dns_denied'")
	if err != nil {
		return err
	}
	hasTable := rows.Next()
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	hasSession, hasWorkspace, hasIgnored, err := columns(db, "dns_denied")
	if err != nil {
		return err
	}
	sessionCol := "''"
	if hasSession {
		sessionCol = "session"
	}
	workspaceCol := "''"
	if hasWorkspace {
		workspaceCol = "workspace"
	}
	ignoredCol := "0"
	if hasIgnored {
		ignoredCol = "COALESCE(ignored, 0)"
	}
	merge := `INSERT INTO denied (host, session, workspace, target, count, first_seen, last_seen, allowed, allowed_at, ignored)
		SELECT host, ` + sessionCol + `, ` + workspaceCol + `, target, count, first_seen, last_seen, allowed, allowed_at, ` + ignoredCol + ` FROM dns_denied WHERE true
		ON CONFLICT(host, session) DO UPDATE SET
			count = denied.count + excluded.count,
			last_seen = MAX(denied.last_seen, excluded.last_seen),
			allowed = MAX(denied.allowed, excluded.allowed)`
	if _, err := db.Exec(merge); err != nil {
		return fmt.Errorf("merge dns_denied into denied: %w", err)
	}
	if _, err := db.Exec("DROP TABLE dns_denied"); err != nil {
		return err
	}
	return nil
}

func columns(db *sql.DB, table string) (session, workspace, ignored bool, err error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, false, false, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, false, false, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return false, false, false, err
	}
	for _, n := range names {
		switch n {
		case "session":
			session = true
		case "workspace":
			workspace = true
		case "ignored":
			ignored = true
		}
	}
	return session, workspace, ignored, nil
}

func rebuild(db *sql.DB, table string) error {
	tmp := table + "_new"
	queries := []string{
		`CREATE TABLE ` + tmp + ` (
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
			PRIMARY KEY (host, session)
		)`,
		`INSERT INTO ` + tmp + ` (host, session, workspace, target, count, first_seen, last_seen, allowed, allowed_at, ignored)
			SELECT host, '', '', target, count, first_seen, last_seen, allowed, allowed_at, COALESCE(ignored, 0) FROM ` + table,
		`DROP TABLE ` + table,
		`ALTER TABLE ` + tmp + ` RENAME TO ` + table,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
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

const deniedCols = "host, session, workspace, target, count, first_seen, last_seen, allowed, allowed_at, ignored"

func Pending(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where("allowed = 0")
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY count DESC, host ASC`, args...)
}

func All(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where()
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY allowed ASC, count DESC, host ASC`, args...)
}

func Ignored(db *sql.DB, f Filter) ([]DeniedHost, error) {
	where, args := f.where("ignored = 1")
	return query(db, `SELECT `+deniedCols+` FROM denied`+where+` ORDER BY last_seen DESC, host ASC`, args...)
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
		if err := rows.Scan(&h.Host, &h.Session, &h.Workspace, &h.Target, &h.Count, &h.FirstSeen, &h.LastSeen, &allowed, &allowedAt, &ignored); err != nil {
			return nil, err
		}
		h.Allowed = allowed != 0
		h.AllowedAt = allowedAt.String
		h.Ignored = ignored != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
