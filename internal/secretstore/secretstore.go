package secretstore

import (
	"database/sql"
	"errors"
	"fmt"
	"hsh/internal/config"
	"hsh/internal/dbschema"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`

var ErrNotFound = config.ErrSecretNotFound
var ErrExists = errors.New("secret already exists (use --force to overwrite)")

type Entry struct {
	Name      string
	CreatedAt string
	UpdatedAt string
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
	if err := dbschema.Apply(db, "secrets.db", path, SchemaVersion, schema); err != nil {
		db.Close()
		return nil, false, fmt.Errorf("secret store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, false, err
	}
	return &DB{db: db}, created, nil
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
	res, err := d.db.Exec(`DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) List() ([]Entry, error) {
	rows, err := d.db.Query(`SELECT name, created_at, updated_at FROM secrets ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Name, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) Close() error {
	return d.db.Close()
}
