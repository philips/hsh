package monitordb

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"hsh/internal/config"
)

const (
	AccessAllow = "allow"
	AccessDeny  = "deny"

	LogRecord = "record"
	LogReview = "review"

	SourceConfig = "config"
	SourceUser   = "user"
	policyPrefix = "policy:"
)

type Rule struct {
	Pattern   string
	Source    string
	Access    string
	Log       string
	DecidedAt string
}

func (r Rule) Policy() string {
	return strings.TrimPrefix(r.Source, policyPrefix)
}

func (r Rule) FromPolicy() bool {
	return strings.HasPrefix(r.Source, policyPrefix)
}

func (r Rule) DeniedBy() string {
	if r.FromPolicy() {
		return "denied by policy " + r.Policy()
	}
	return "denied by you"
}

type Match struct {
	Access string
	Log    string
	Rule   Rule
}

func Evaluate(rules []Rule, host string) Match {
	var m Match
	var deny, allow *Rule
	for i := range rules {
		r := &rules[i]
		if !config.HostMatches([]string{r.Pattern}, host) {
			continue
		}
		switch r.Access {
		case AccessDeny:
			if deny == nil || (r.FromPolicy() && !deny.FromPolicy()) {
				deny = r
			}
		case AccessAllow:
			if allow == nil {
				allow = r
			}
		}
		switch r.Log {
		case LogRecord:
			m.Log = LogRecord
		case LogReview:
			if m.Log == "" {
				m.Log = LogReview
			}
		}
	}
	switch {
	case deny != nil:
		m.Access, m.Rule = AccessDeny, *deny
	case allow != nil:
		m.Access, m.Rule = AccessAllow, *allow
	}
	return m
}

func queryRules(db *sql.DB) ([]Rule, error) {
	rows, err := db.Query(`SELECT pattern, source, access, log, decided_at FROM rules ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var access, logv sql.NullString
		if err := rows.Scan(&r.Pattern, &r.Source, &access, &logv, &r.DecidedAt); err != nil {
			return nil, err
		}
		r.Access, r.Log = access.String, logv.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func readRules(sessionDir string) ([]Rule, error) {
	path := MonitorDBPath(sessionDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := openReadOnly(path, 5000)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := checkVersion(db, MonitorDBFile, MonitorSchemaV); err != nil {
		return nil, err
	}
	return queryRules(db)
}

func (s *Store) Rules(session string) ([]Rule, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("invalid session id %q", session)
	}
	return readRules(s.sessionDir(session))
}

func (s *Store) Allow(session string, patterns []string) error {
	return s.setUser(session, patterns, AccessAllow)
}

func (s *Store) Deny(session string, patterns []string) error {
	return s.setUser(session, patterns, AccessDeny)
}

func (s *Store) setUser(session string, patterns []string, access string) error {
	if len(patterns) == 0 {
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
	return insertRules(db, patterns, SourceUser, access)
}

func insertRules(db *sql.DB, patterns []string, source, access string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	for _, p := range patterns {
		if _, err := tx.Exec(`INSERT INTO rules (pattern, source, access, log, decided_at) VALUES (?, ?, ?, NULL, ?)
			ON CONFLICT(pattern, source) DO UPDATE SET access = excluded.access, log = excluded.log, decided_at = excluded.decided_at`, p, source, access, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func InitSession(sessionDir string, allow []string) error {
	path := MonitorDBPath(sessionDir)
	fresh := !regularFile(path)
	db, err := openWritable(path, MonitorDBFile, MonitorSchemaV, monitorSchema)
	if err != nil {
		return err
	}
	defer db.Close()
	if fresh {
		if err := insertRules(db, allow, SourceConfig, AccessAllow); err != nil {
			return err
		}
	}
	return nil
}

const rulesTTL = 200 * time.Millisecond

type RuleReader struct {
	path    string
	mu      sync.Mutex
	db      *sql.DB
	rules   []Rule
	loaded  time.Time
	lastErr string
	logf    func(format string, args ...any)
}

func NewRuleReader(path string, logf func(format string, args ...any)) *RuleReader {
	return &RuleReader{path: path, logf: logf}
}

func (r *RuleReader) Rules() []Rule {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.loaded) < rulesTTL {
		return r.rules
	}
	r.loaded = time.Now()
	rules, err := r.load()
	if err != nil {
		if msg := err.Error(); msg != r.lastErr {
			r.lastErr = msg
			if r.logf != nil {
				r.logf("session rules unavailable, using the network mode's defaults: %v", err)
			}
		}
		r.rules = nil
		return nil
	}
	if r.lastErr != "" {
		r.lastErr = ""
		if r.logf != nil {
			r.logf("session rules loaded: %d rule(s)", len(rules))
		}
	}
	r.rules = rules
	return rules
}

func (r *RuleReader) load() ([]Rule, error) {
	if r.db == nil {
		if !regularFile(r.path) {
			return nil, fmt.Errorf("%s is missing", r.path)
		}
		db, err := openReadOnly(r.path, 1000)
		if err != nil {
			return nil, err
		}
		if err := checkVersion(db, MonitorDBFile, MonitorSchemaV); err != nil {
			db.Close()
			return nil, err
		}
		r.db = db
	}
	rules, err := queryRules(r.db)
	if err != nil {
		r.db.Close()
		r.db = nil
		return nil, err
	}
	return rules, nil
}

func (r *RuleReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
}
