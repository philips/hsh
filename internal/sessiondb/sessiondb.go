package sessiondb

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"hsh/internal/version"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
	id      INTEGER PRIMARY KEY,
	time    TEXT NOT NULL,
	kind    TEXT NOT NULL,
	version TEXT NOT NULL
);
`

func Append(sessionDir, kind string) error {
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(sessionDir, "session.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	defer db.Close()
	insert := func() error {
		if _, err := db.Exec(schema); err != nil {
			return err
		}
		_, err := db.Exec(
			"INSERT INTO events (time, kind, version) VALUES (?, ?, ?)",
			time.Now().UTC().Format(time.RFC3339), kind, version.Get().String(),
		)
		return err
	}
	err = insert()
	for i := 0; i < 40 && err != nil && strings.Contains(err.Error(), "SQLITE_BUSY"); i++ {
		time.Sleep(25 * time.Millisecond)
		err = insert()
	}
	return err
}
