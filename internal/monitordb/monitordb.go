package monitordb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	State     string
	Source    string
	DecidedAt string
	DeniedBy  string
	Holding   int
}

const (
	StatePending = "pending"
	StateAllowed = "allowed"
	StateDenied  = "denied"
	StateQuiet   = "quiet"
)

func (h DeniedHost) Pending() bool { return h.State == StatePending }

func (h DeniedHost) Denied() bool { return h.State == StateDenied }

func (h DeniedHost) DeniedByPolicy() bool {
	return h.Denied() && strings.HasPrefix(h.Source, policyPrefix)
}

func stateOf(m Match) string {
	switch {
	case m.Access == AccessDeny:
		return StateDenied
	case m.Access == AccessAllow:
		return StateAllowed
	case m.Log == LogRecord:
		return StateQuiet
	}
	return StatePending
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
		rules, err := readRules(dir)
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
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
			m := Evaluate(rules, d.host)
			h.State = stateOf(m)
			h.Source, h.DecidedAt = m.Rule.Source, m.Rule.DecidedAt
			if h.State == StateDenied {
				h.DeniedBy = m.Rule.DeniedBy()
			}
			if h.State != StatePending {
				h.Holding = 0
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
		if r.Pending() {
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
		if x.Pending() != y.Pending() {
			return x.Pending()
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

func (s *Store) Denied(f Filter) ([]DeniedHost, error) {
	rows, err := s.rows(f)
	if err != nil {
		return nil, err
	}
	var out []DeniedHost
	for _, r := range rows {
		if r.Denied() {
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
