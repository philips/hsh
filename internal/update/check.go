package update

import (
	"database/sql"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	kvSchema = `
CREATE TABLE IF NOT EXISTS kv (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

	kvLastCheck = "update_last_check"
	kvLatestTag = "update_latest_tag"
	kvAuto      = "update_auto"
)

func tagVersion(tag string) ([3]int, bool) {
	var v [3]int
	tag = strings.TrimPrefix(tag, "v")
	parts := strings.Split(tag, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || p[0] == '+' || p[0] == '-' {
			return v, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func Newer(current, latest string) bool {
	lv, ok := tagVersion(latest)
	if !ok {
		return false
	}
	cv, ok := tagVersion(current)
	if !ok {
		return true
	}
	for i := range lv {
		if lv[i] != cv[i] {
			return lv[i] > cv[i]
		}
	}
	return false
}

func kvSet(db *sql.DB, name, value string) error {
	return busyRetry(func() error {
		_, err := db.Exec(
			"INSERT INTO kv (name, value) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value",
			name, value,
		)
		return err
	})
}

func kvDelete(db *sql.DB, name string) error {
	return busyRetry(func() error {
		_, err := db.Exec("DELETE FROM kv WHERE name = ?", name)
		return err
	})
}

func kvGet(path, name string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	db, err := openCounter(path)
	if err != nil {
		return ""
	}
	defer db.Close()
	var value string
	switch err := db.QueryRow("SELECT value FROM kv WHERE name = ?", name).Scan(&value); err {
	case nil:
		return value
	default:
		return ""
	}
}

func MarkCheck(path string) error {
	db, err := openCounter(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return kvSet(db, kvLastCheck, strconv.FormatInt(time.Now().Unix(), 10))
}

func SetLatest(path, tag string) error {
	db, err := openCounter(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return kvSet(db, kvLatestTag, tag)
}

func ClearLatest(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := openCounter(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return kvDelete(db, kvLatestTag)
}

func KnownLatest(path string) string {
	return kvGet(path, kvLatestTag)
}

func CheckDue(path string, interval time.Duration) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := openCounter(path)
	if err != nil {
		return false
	}
	defer db.Close()
	var value string
	switch err := db.QueryRow("SELECT value FROM kv WHERE name = ?", kvLastCheck).Scan(&value); err {
	case nil:
		last, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return true
		}
		return time.Since(time.Unix(last, 0)) >= interval
	case sql.ErrNoRows:
		return true
	default:
		return false
	}
}

func AutoEnabled(path string) bool {
	return kvGet(path, kvAuto) == "1"
}

func SetAuto(path string, on bool) error {
	db, err := openCounter(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if on {
		return kvSet(db, kvAuto, "1")
	}
	return kvDelete(db, kvAuto)
}
