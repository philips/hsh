package monitordbtest

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"hsh/internal/monitordb"
)

func Seed(sessionDir string, rows ...monitordb.DeniedHost) error {
	path := monitordb.ProxyDBPath(sessionDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	rec, err := monitordb.OpenRecorder(path)
	if err != nil {
		return err
	}
	for _, r := range rows {
		target := r.Target
		if target == "" {
			target = r.Host + ":443"
		}
		if err := rec.Record(r.Host, target); err != nil {
			rec.Close()
			return err
		}
	}
	if err := rec.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, r := range rows {
		count, last, first := r.Count, r.LastSeen, r.FirstSeen
		if count == 0 {
			count = 1
		}
		if last == "" {
			last = time.Now().UTC().Format(time.RFC3339)
		}
		if first == "" {
			first = last
		}
		if _, err := db.Exec(`UPDATE denied SET count = ?, first_seen = ?, last_seen = ?, holding = ? WHERE host = ?`, count, first, last, r.Holding, r.Host); err != nil {
			return err
		}
	}
	return nil
}
