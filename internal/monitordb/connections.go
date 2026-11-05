package monitordb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"hsh/internal/config"
)

const (
	ConnLogFile    = "connections.db"
	ConnLogSchemaV = 1
	maxConnLogDB   = 32 << 20
)

var MaxConnections = 50000

const connLogSchema = `
CREATE TABLE IF NOT EXISTS connections (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	time        TEXT NOT NULL,
	host        TEXT NOT NULL,
	port        INTEGER NOT NULL,
	scheme      TEXT NOT NULL,
	access      TEXT NOT NULL,
	log         TEXT NOT NULL,
	pattern     TEXT,
	source      TEXT,
	held_ms     INTEGER NOT NULL DEFAULT 0,
	bytes_up    INTEGER NOT NULL DEFAULT 0,
	bytes_down  INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0
);
`

const (
	SchemeTLS  = "tls"
	SchemeHTTP = "http"
	SchemeTCP  = "tcp"
)

type Connection struct {
	ID         int64  `json:"id"`
	Session    string `json:"session"`
	Time       string `json:"time"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Scheme     string `json:"scheme"`
	Access     string `json:"access"`
	Log        string `json:"log"`
	Pattern    string `json:"pattern"`
	Source     string `json:"source"`
	HeldMS     int64  `json:"held_ms"`
	BytesUp    int64  `json:"bytes_up"`
	BytesDown  int64  `json:"bytes_down"`
	DurationMS int64  `json:"duration_ms"`
}

func ConnLogPath(sessionDir string) string {
	return filepath.Join(sessionDir, "proxy", ConnLogFile)
}

type ConnLog struct {
	db *sql.DB
}

func OpenConnLog(path string) (*ConnLog, error) {
	db, err := openWritable(path, ConnLogFile, ConnLogSchemaV, connLogSchema)
	if err != nil {
		return nil, err
	}
	return &ConnLog{db: db}, nil
}

func (l *ConnLog) Close() error { return l.db.Close() }

func (l *ConnLog) Insert(c Connection) (int64, error) {
	if !ValidHost(c.Host) {
		return 0, fmt.Errorf("not logging a malformed host %q", c.Host)
	}
	res, err := l.db.Exec(`INSERT INTO connections (time, host, port, scheme, access, log, pattern, source, held_ms, bytes_up, bytes_down, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
		c.Time, c.Host, c.Port, c.Scheme, c.Access, c.Log, c.Pattern, c.Source, c.HeldMS, c.BytesUp, c.BytesDown, c.DurationMS)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if limit := int64(MaxConnections); id > limit {
		if _, err := l.db.Exec(`DELETE FROM connections WHERE id <= ?`, id-limit); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (l *ConnLog) Update(c Connection) error {
	_, err := l.db.Exec(`UPDATE connections SET access = ?, log = ?, pattern = NULLIF(?, ''), source = NULLIF(?, ''), held_ms = ?, bytes_up = ?, bytes_down = ?, duration_ms = ? WHERE id = ?`,
		c.Access, c.Log, c.Pattern, c.Source, c.HeldMS, c.BytesUp, c.BytesDown, c.DurationMS, c.ID)
	return err
}

var policyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validSource(source string) bool {
	switch source {
	case SourceConfig, SourceUser:
		return true
	}
	name, ok := strings.CutPrefix(source, policyPrefix)
	return ok && policyNamePattern.MatchString(name)
}

func validConnection(c Connection) bool {
	if !ValidHost(c.Host) || c.Port < 0 || c.Port > 65535 || c.ID <= 0 {
		return false
	}
	switch c.Scheme {
	case SchemeTLS, SchemeHTTP, SchemeTCP:
	default:
		return false
	}
	switch c.Access {
	case AccessAllow, AccessDeny:
	default:
		return false
	}
	switch c.Log {
	case LogRecord, LogReview:
	default:
		return false
	}
	if c.Pattern != "" {
		if _, err := config.CanonicalPattern(c.Pattern); err != nil {
			return false
		}
	}
	if c.Source != "" && !validSource(c.Source) {
		return false
	}
	if _, err := time.Parse(time.RFC3339, c.Time); err != nil {
		return false
	}
	return c.HeldMS >= 0 && c.BytesUp >= 0 && c.BytesDown >= 0 && c.DurationMS >= 0
}

func (s *Store) withConnLog(session string, fn func(ctx context.Context, conn *sql.Conn) error) error {
	if !validSession(session) {
		return fmt.Errorf("invalid session id %q", session)
	}
	path := ConnLogPath(s.sessionDir(session))
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	data, err := snapshotFile(path, maxConnLogDB, untrustedTimeout*time.Millisecond)
	if err != nil {
		return fmt.Errorf("session %s: %s: %w", session, ConnLogFile, err)
	}
	err = withSnapshot(data, func(conn *sql.Conn) error {
		ctx := context.Background()
		var have int
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
			return err
		}
		if have != ConnLogSchemaV {
			return fmt.Errorf("%s has schema version %d, this hsh reads version %d", ConnLogFile, have, ConnLogSchemaV)
		}
		return fn(ctx, conn)
	})
	if err != nil {
		return fmt.Errorf("session %s: %w", session, err)
	}
	return nil
}

func (s *Store) Connections(session string, since int64, limit int) ([]Connection, error) {
	var out []Connection
	err := s.withConnLog(session, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT id, time, host, port, scheme, access, log, pattern, source, held_ms, bytes_up, bytes_down, duration_ms
			FROM connections WHERE id > ? ORDER BY id LIMIT ?`, since, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t, host, scheme, access, logv, pattern, source sql.NullString
			var id, port, held, up, down, dur sql.NullInt64
			if err := rows.Scan(&id, &t, &host, &port, &scheme, &access, &logv, &pattern, &source, &held, &up, &down, &dur); err != nil {
				return err
			}
			c := Connection{
				ID: id.Int64, Session: session, Time: t.String, Host: host.String, Port: int(port.Int64),
				Scheme: scheme.String, Access: access.String, Log: logv.String, Pattern: pattern.String, Source: source.String,
				HeldMS: held.Int64, BytesUp: up.Int64, BytesDown: down.Int64, DurationMS: dur.Int64,
			}
			if validConnection(c) {
				out = append(out, c)
			}
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) LastConnectionID(session string) (int64, error) {
	var last sql.NullInt64
	err := s.withConnLog(session, func(ctx context.Context, conn *sql.Conn) error {
		return conn.QueryRowContext(ctx, `SELECT MAX(id) FROM connections`).Scan(&last)
	})
	return max(0, last.Int64), err
}

func (s *Store) ConnLogChanged(session string, since time.Time, size int64) (time.Time, int64, bool) {
	fi, err := os.Lstat(ConnLogPath(s.sessionDir(session)))
	if err != nil {
		return since, size, false
	}
	changed := !fi.ModTime().Equal(since) || fi.Size() != size
	return fi.ModTime(), fi.Size(), changed
}
