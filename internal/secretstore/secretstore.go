package secretstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"hsh/internal/config"
	"hsh/internal/dbschema"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 3

const oauthSchema = `
CREATE TABLE IF NOT EXISTS oauth_secrets (
    name         TEXT PRIMARY KEY,
    provider     TEXT NOT NULL,
    tokens       TEXT NOT NULL,
    meta         TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    refreshed_at TEXT NOT NULL,
    failure      TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
`

const schema = `
CREATE TABLE IF NOT EXISTS secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    hosts      TEXT NOT NULL DEFAULT '[]'
);
` + oauthSchema

const hostsMigration = `ALTER TABLE secrets ADD COLUMN hosts TEXT NOT NULL DEFAULT '[]';`

var migrations = map[int]string{1: oauthSchema, 2: hostsMigration}

var ErrNotFound = config.ErrSecretNotFound
var ErrExists = errors.New("secret already exists (use --force to overwrite)")

type Entry struct {
	Name      string
	Kind      string
	Provider  string
	CreatedAt string
	UpdatedAt string
	ExpiresAt string
	Failure   string
	Hosts     []string
}

type Store interface {
	Get(name string) (string, error)
	Set(name, value string, overwrite bool) error
	Delete(name string) error
	List() ([]Entry, error)
	Close() error
}

func ConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "hsh")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hsh-config"
	}
	return filepath.Join(home, ".config", "hsh")
}

func DefaultPath() string {
	return filepath.Join(ConfigDir(), "secrets.db")
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, bool, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
	}
	created := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		created = true
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, false, err
	}
	if err := dbschema.ApplyWithMigrations(db, "secrets.db", path, SchemaVersion, schema, migrations); err != nil {
		db.Close()
		return nil, false, fmt.Errorf("secret store: %w", err)
	}
	if err := ensureHostsColumn(db); err != nil {
		db.Close()
		return nil, false, fmt.Errorf("secret store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, false, err
	}
	return &DB{db: db}, created, nil
}

func ensureHostsColumn(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('secrets')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return err
		}
		if col == "hosts" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(hostsMigration)
	return err
}

func (d *DB) Get(name string) (string, error) {
	var value string
	err := d.db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

func (d *DB) Set(name, value string, overwrite bool) error {
	if _, err := d.GetOAuth(name); err == nil {
		return fmt.Errorf("%q is an OAuth login (hsh oauth promote); remove it first with: hsh secret rm %s", name, name)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if overwrite {
		_, err := d.db.Exec(`INSERT INTO secrets (name, value, created_at, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			name, value, now, now)
		return err
	}
	res, err := d.db.Exec(`INSERT INTO secrets (name, value, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO NOTHING`, name, value, now, now)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrExists
	}
	return nil
}

func (d *DB) Delete(name string) error {
	var total int64
	for _, q := range []string{`DELETE FROM secrets WHERE name = ?`, `DELETE FROM oauth_secrets WHERE name = ?`} {
		res, err := d.db.Exec(q, name)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		total += n
	}
	if total == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) List() ([]Entry, error) {
	rows, err := d.db.Query(`SELECT name, 'static', '', created_at, updated_at, '', '', hosts FROM secrets
		UNION ALL
		SELECT name, 'oauth', provider, created_at, refreshed_at, expires_at, failure, '[]' FROM oauth_secrets
		ORDER BY 1 ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var hosts string
		if err := rows.Scan(&e.Name, &e.Kind, &e.Provider, &e.CreatedAt, &e.UpdatedAt, &e.ExpiresAt, &e.Failure, &hosts); err != nil {
			return nil, err
		}
		if e.Hosts, err = decodeHosts(e.Name, hosts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func decodeHosts(name, raw string) ([]string, error) {
	var hosts []string
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		return nil, fmt.Errorf("secret %s: hosts: %w", name, err)
	}
	return hosts, nil
}

func (d *DB) SecretHosts(name string) ([]string, error) {
	var raw string
	err := d.db.QueryRow(`SELECT hosts FROM secrets WHERE name = ?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := d.GetOAuth(name); err == nil {
			return nil, fmt.Errorf("%q is an OAuth login; its hosts come from the provider descriptor (hsh oauth provider cat)", name)
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeHosts(name, raw)
}

func NormalizeHosts(hosts []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, h := range hosts {
		n, err := config.NormalizeSecretHost(h)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *DB) SetHosts(name string, hosts []string) error {
	norm, err := NormalizeHosts(hosts)
	if err != nil {
		return err
	}
	data, err := json.Marshal(norm)
	if err != nil {
		return err
	}
	res, err := d.db.Exec(`UPDATE secrets SET hosts = ?, updated_at = ? WHERE name = ?`, string(data), time.Now().UTC().Format(time.RFC3339), name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		_, err := d.SecretHosts(name)
		return err
	}
	return nil
}

func (d *DB) AllowHosts(name string, hosts ...string) ([]string, error) {
	cur, err := d.SecretHosts(name)
	if err != nil {
		return nil, err
	}
	next := append(append([]string{}, cur...), hosts...)
	if err := d.SetHosts(name, next); err != nil {
		return nil, err
	}
	return d.SecretHosts(name)
}

func (d *DB) DisallowHosts(name string, hosts ...string) ([]string, error) {
	cur, err := d.SecretHosts(name)
	if err != nil {
		return nil, err
	}
	drop := map[string]bool{}
	for _, h := range hosts {
		n, err := config.NormalizeSecretHost(h)
		if err != nil {
			return nil, err
		}
		drop[n] = true
	}
	var next []string
	for _, h := range cur {
		if !drop[h] {
			next = append(next, h)
		}
	}
	if err := d.SetHosts(name, next); err != nil {
		return nil, err
	}
	return d.SecretHosts(name)
}

func (d *DB) Close() error {
	return d.db.Close()
}
