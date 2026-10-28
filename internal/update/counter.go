package update

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const counterSchema = `
CREATE TABLE IF NOT EXISTS counters (
	name  TEXT PRIMARY KEY,
	value INTEGER NOT NULL DEFAULT 0
);
`

const sessionsCounter = "sessions_since_last_update"

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

func DefaultPath() string {
	return filepath.Join(StateDir(), "metrics.db")
}

func openCounter(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(counterSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Bump(path string) error {
	db, err := openCounter(path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(
		"INSERT INTO counters (name, value) VALUES (?, 1) ON CONFLICT(name) DO UPDATE SET value = value + 1",
		sessionsCounter,
	)
	return err
}

func Take(path string) (int, error) {
	db, err := openCounter(path)
	if err != nil {
		return 0, nil
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, nil
	}
	var value int
	row := tx.QueryRow("SELECT value FROM counters WHERE name = ?", sessionsCounter)
	switch err := row.Scan(&value); err {
	case nil:
	case sql.ErrNoRows:
		if err := tx.Rollback(); err != nil {
			return 0, nil
		}
		return 0, nil
	default:
		tx.Rollback()
		return 0, nil
	}
	if _, err := tx.Exec("UPDATE counters SET value = 0 WHERE name = ?", sessionsCounter); err != nil {
		tx.Rollback()
		return 0, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, nil
	}
	return value, nil
}
