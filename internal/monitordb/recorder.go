package monitordb

import (
	"database/sql"
	"fmt"
	"sync"
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

type Decisions struct {
	path string
	mu   sync.Mutex
	db   *sql.DB
}

func NewDecisions(path string) *Decisions {
	return &Decisions{path: path}
}

func (d *Decisions) open() *sql.DB {
	if d.db != nil {
		return d.db
	}
	if !regularFile(d.path) {
		return nil
	}
	db, err := openReadOnly(d.path, 1000)
	if err != nil {
		return nil
	}
	if err := checkVersion(db, MonitorDBFile, MonitorSchemaV); err != nil {
		db.Close()
		return nil
	}
	d.db = db
	return db
}

func (d *Decisions) Lookup(host string) Decision {
	d.mu.Lock()
	defer d.mu.Unlock()
	db := d.open()
	if db == nil {
		return Decision{}
	}
	var ignored int
	switch err := db.QueryRow(`SELECT ignored FROM decisions WHERE host = ?`, host).Scan(&ignored); err {
	case nil:
		return Decision{Found: true, Allowed: true, Ignored: ignored != 0}
	case sql.ErrNoRows:
		return Decision{}
	default:
		return Decision{}
	}
}

func (d *Decisions) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		d.db.Close()
		d.db = nil
	}
}
