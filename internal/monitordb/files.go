package monitordb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	_ "modernc.org/sqlite"

	"hsh/internal/dbschema"
)

const (
	ProxyDBFile      = "proxy.db"
	MonitorDBFile    = "monitor.db"
	ProxySchemaV     = 1
	MonitorSchemaV   = 2
	MaxHosts         = 4096
	untrustedTimeout = 250
)

const proxySchema = `
CREATE TABLE IF NOT EXISTS denied (
	host       TEXT PRIMARY KEY,
	target     TEXT NOT NULL,
	count      INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen  TEXT NOT NULL,
	holding    INTEGER NOT NULL DEFAULT 0
);
`

const monitorSchema = `
CREATE TABLE IF NOT EXISTS rules (
	pattern    TEXT NOT NULL,
	source     TEXT NOT NULL,
	access     TEXT,
	log        TEXT,
	decided_at TEXT NOT NULL,
	PRIMARY KEY (pattern, source)
);
`

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9._\-:]{1,253}$`)
var targetPattern = regexp.MustCompile(`^[A-Za-z0-9._\-:\[\]]{1,262}$`)

func ValidHost(host string) bool { return hostPattern.MatchString(host) }

func validTarget(target string) bool { return targetPattern.MatchString(target) }

func ProxyDBPath(sessionDir string) string {
	return filepath.Join(sessionDir, "proxy", ProxyDBFile)
}

func MonitorDBPath(sessionDir string) string {
	return filepath.Join(sessionDir, MonitorDBFile)
}

func openWritable(path, name string, version int, schema string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(DELETE)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbschema.Apply(db, name, path, version, schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openReadOnly(path string, timeoutMS int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(1)&_pragma=trusted_schema(0)", path, timeoutMS))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func checkVersion(db *sql.DB, name string, want int) error {
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	switch {
	case have > want:
		return fmt.Errorf("%s has schema version %d, newer than this hsh reads (version %d); update hsh", name, have, want)
	case have != want:
		return fmt.Errorf("%s has schema version %d, this hsh reads version %d", name, have, want)
	}
	return nil
}

func regularFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
