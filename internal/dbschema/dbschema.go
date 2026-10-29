package dbschema

import (
	"context"
	"database/sql"
	"fmt"
)

func Apply(db *sql.DB, name, path string, version int, schema string) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	var have int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	switch {
	case have == version:
	case have > version:
		return fmt.Errorf("%s has schema version %d, newer than this hsh reads (version %d); update hsh", name, have, version)
	case have != 0:
		return fmt.Errorf("%s has schema version %d, older than this hsh reads (version %d); remove %s", name, have, version, path)
	default:
		if _, err = conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
