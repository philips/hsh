package monitordb

import (
	"database/sql"
	"fmt"
)

type Recorder struct {
	db *sql.DB
}

func OpenRecorder(path string) (*Recorder, error) {
	db, err := openWritable(path, ProxyDBFile, ProxySchemaV, proxySchema)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE denied SET holding = 0`); err != nil {
		db.Close()
		return nil, err
	}
	return &Recorder{db: db}, nil
}

func (r *Recorder) Close() error { return r.db.Close() }

func (r *Recorder) Record(host, target string) error {
	if !ValidHost(host) || !validTarget(target) {
		return fmt.Errorf("not recording a malformed host %q", host)
	}
	t := now()
	res, err := r.db.Exec(`UPDATE denied SET count = count + 1, last_seen = ? WHERE host = ?`, t, host)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM denied`).Scan(&total); err != nil {
		return err
	}
	if total >= MaxHosts {
		return fmt.Errorf("not recording %s: %d hosts already recorded", host, MaxHosts)
	}
	_, err = r.db.Exec(`INSERT INTO denied (host, target, count, first_seen, last_seen) VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(host) DO UPDATE SET count = denied.count + 1, last_seen = excluded.last_seen`, host, target, t, t)
	return err
}

func (r *Recorder) AdjustHolding(host string, delta int) error {
	_, err := r.db.Exec(`UPDATE denied SET holding = MAX(0, holding + ?) WHERE host = ?`, delta, host)
	return err
}
