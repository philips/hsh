package monitordb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"


	"hsh/internal/config"
	"hsh/internal/state"
)

type DeniedHost struct {
	Host      string
	Session   string
	Workspace string
	Target    string
	Count     int
	FirstSeen string
	LastSeen  string
	Allowed   bool
	AllowedAt string
	Ignored   bool
	Holding   int
}

type Filter struct {
	Session   string
	Workspace string
}

type Store struct {
	dir string
}

func NewStore(sessionsDir string) *Store { return &Store{dir: sessionsDir} }

func DefaultStore() *Store { return NewStore(state.SessionsDir()) }

func validSession(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

func (s *Store) sessionDir(id string) string { return filepath.Join(s.dir, id) }

func (s *Store) sessions(f Filter) []string {
	if f.Session != "" {
		if !validSession(f.Session) {
			return nil
		}
		if fi, err := os.Stat(s.sessionDir(f.Session)); err != nil || !fi.IsDir() {
			return nil
		}
		return []string{f.Session}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && validSession(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func readWorkspace(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "workspace"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

type denial struct {
	host, target, firstSeen, lastSeen string
	count, holding                    int
}

const maxProxyDB = 8 << 20

func readDenials(sessionDir string) []denial {
	data, err := snapshotFile(ProxyDBPath(sessionDir), maxProxyDB, untrustedTimeout*time.Millisecond)
	if err != nil {
		return nil
	}
	var out []denial
	err = withSnapshot(data, func(conn *sql.Conn) error {
		ctx := context.Background()
		var have int
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
			return err
		}
		if have != ProxySchemaV {
			return fmt.Errorf("%s has schema version %d, this hsh reads version %d", ProxyDBFile, have, ProxySchemaV)
		}
		rows, err := conn.QueryContext(ctx, `SELECT host, target, count, first_seen, last_seen, holding FROM denied LIMIT ?`, MaxHosts)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var host, target, first, last sql.NullString
			var count, holding sql.NullInt64
			if err := rows.Scan(&host, &target, &count, &first, &last, &holding); err != nil {
				return err
			}
			out = append(out, denial{
				host: host.String, target: target.String, firstSeen: first.String, lastSeen: last.String,
				count: int(max(0, count.Int64)), holding: int(max(0, holding.Int64)),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil
	}
	return out
}

type decision struct {
	ignored bool
	at      string
}

func readDecisions(sessionDir string) (map[string]decision, error) {
	out := map[string]decision{}
	path := MonitorDBPath(sessionDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	db, err := openReadOnly(path, 5000)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := checkVersion(db, MonitorDBFile, MonitorSchemaV); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT host, ignored, decided_at FROM decisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var host, at string
		var ignored int
		if err := rows.Scan(&host, &ignored, &at); err != nil {
			return nil, err
		}
		out[host] = decision{ignored: ignored != 0, at: at}
	}
	return out, rows.Err()
}

func readAllowList(sessionDir string) []string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "config.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Network struct {
			Allow []string `json:"allow"`
		} `json:"network"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return doc.Network.Allow
}

func (s *Store) rows(f Filter) ([]DeniedHost, error) {
	var out []DeniedHost
	for _, id := range s.sessions(f) {
		dir := s.sessionDir(id)
		ws := readWorkspace(dir)
		if f.Workspace != "" && ws != f.Workspace {
			continue
		}
		den := readDenials(dir)
		if len(den) == 0 {
			continue
		}
		dec, err := readDecisions(dir)
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
		allow := readAllowList(dir)
		for _, d := range den {
			if !ValidHost(d.host) {
				continue
			}
			target := d.target
			if !validTarget(target) {
				target = d.host
			}
			h := DeniedHost{
				Host: d.host, Session: id, Workspace: ws, Target: target,
				Count: d.count, FirstSeen: d.firstSeen, LastSeen: d.lastSeen, Holding: d.holding,
			}
			if config.HostMatches(allow, d.host) {
				h.Allowed = true
				h.Holding = 0
			}
			if c, ok := dec[d.host]; ok {
				h.AllowedAt = c.at
				h.Ignored = c.ignored
			}
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *Store) Pending(f Filter) ([]DeniedHost, error) {
	rows, err := s.rows(f)
	if err != nil {
		return nil, err
	}
	var out []DeniedHost
	for _, r := range rows {
		if !r.Allowed && !r.Ignored {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if (x.Holding > 0) != (y.Holding > 0) {
			return x.Holding > 0
		}
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		return x.Session < y.Session
	})
	return out, nil
}

func (s *Store) All(f Filter) ([]DeniedHost, error) {
	out, err := s.rows(f)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.Allowed != y.Allowed {
			return !x.Allowed
		}
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		return x.Session < y.Session
	})
	return out, nil
}

func (s *Store) Ignored(f Filter) ([]DeniedHost, error) {
	rows, err := s.rows(f)
	if err != nil {
		return nil, err
	}
	var out []DeniedHost
	for _, r := range rows {
		if r.Ignored {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.LastSeen != y.LastSeen {
			return x.LastSeen > y.LastSeen
		}
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		return x.Session < y.Session
	})
	return out, nil
}

func (s *Store) Lookup(session, host string) (Decision, error) {
	if !validSession(session) {
		return Decision{}, fmt.Errorf("invalid session id %q", session)
	}
	dec, err := readDecisions(s.sessionDir(session))
	if err != nil {
		return Decision{}, err
	}
	if c, ok := dec[host]; ok {
		return Decision{Found: true, Allowed: true, Ignored: c.ignored}, nil
	}
	return Decision{}, nil
}

func (s *Store) MarkAllowed(session string, hosts []string) error {
	return s.decide(session, hosts, false)
}

func (s *Store) MarkIgnored(session string, hosts []string) error {
	return s.decide(session, hosts, true)
}

func InitSession(sessionDir string) error {
	db, err := openWritable(MonitorDBPath(sessionDir), MonitorDBFile, MonitorSchemaV, monitorSchema)
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *Store) decide(session string, hosts []string, ignored bool) error {
	if len(hosts) == 0 {
		return nil
	}
	if !validSession(session) {
		return fmt.Errorf("invalid session id %q", session)
	}
	dir := s.sessionDir(session)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("session %s has no state directory at %s", session, dir)
	}
	db, err := openWritable(MonitorDBPath(dir), MonitorDBFile, MonitorSchemaV, monitorSchema)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	flag := 0
	if ignored {
		flag = 1
	}
	for _, h := range hosts {
		if _, err := tx.Exec(`INSERT INTO decisions (host, ignored, decided_at) VALUES (?, ?, ?)
			ON CONFLICT(host) DO UPDATE SET ignored = excluded.ignored, decided_at = excluded.decided_at`, h, flag, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
