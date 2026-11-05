package monitordb

import "database/sql"

func HoldingCount(sessionDir string) (int, error) {
	path := ProxyDBPath(sessionDir)
	if !regularFile(path) {
		return 0, nil
	}
	db, err := openReadOnly(path, 2000)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var total sql.NullInt64
	if err := db.QueryRow(`SELECT SUM(holding) FROM denied`).Scan(&total); err != nil {
		return 0, err
	}
	if total.Int64 < 0 {
		return 0, nil
	}
	return int(total.Int64), nil
}
