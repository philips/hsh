package dbschema

import (
	"context"
	"database/sql"
	"fmt"
)

func Apply(db *sql.DB, name, path string, version int, schema string) error {
	return ApplyWithMigrations(db, name, path, version, schema, nil)
}

func ApplyWithMigrations(db *sql.DB, name, path string, version int, schema string, migrations map[int]string) (err error) {
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
	case have != 0 && canMigrate(have, version, migrations):
		for v := have; v < version; v++ {
			if _, err = conn.ExecContext(ctx, migrations[v]); err != nil {
				return fmt.Errorf("%s: migrate schema version %d to %d: %w", name, v, v+1, err)
			}
		}
		if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			return err
		}
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

func canMigrate(from, to int, migrations map[int]string) bool {
	for v := from; v < to; v++ {
		if _, ok := migrations[v]; !ok {
			return false
		}
	}
	return true
}
