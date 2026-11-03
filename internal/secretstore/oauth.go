package secretstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"hsh/internal/config"
)

type OAuthSecret = config.OAuthSecret

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOAuth(r rowScanner) (OAuthSecret, error) {
	var s OAuthSecret
	var tokens, meta, expires, refreshed, created string
	if err := r.Scan(&s.Name, &s.Provider, &tokens, &meta, &expires, &refreshed, &s.Failure, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s, ErrNotFound
		}
		return s, err
	}
	if err := json.Unmarshal([]byte(tokens), &s.Tokens); err != nil {
		return s, fmt.Errorf("oauth secret %s: tokens: %w", s.Name, err)
	}
	if err := json.Unmarshal([]byte(meta), &s.Meta); err != nil {
		return s, fmt.Errorf("oauth secret %s: meta: %w", s.Name, err)
	}
	s.ExpiresAt = parseTime(expires)
	s.RefreshedAt = parseTime(refreshed)
	s.CreatedAt = parseTime(created)
	return s, nil
}

const oauthColumns = `name, provider, tokens, meta, expires_at, refreshed_at, failure, created_at`

func (d *DB) GetOAuth(name string) (OAuthSecret, error) {
	return scanOAuth(d.db.QueryRow(`SELECT `+oauthColumns+` FROM oauth_secrets WHERE name = ?`, name))
}

func (d *DB) PutOAuth(s OAuthSecret, overwrite bool) error {
	if _, err := d.Get(s.Name); err == nil {
		return fmt.Errorf("%q is a static secret; remove it first with: hsh secret rm %s", s.Name, s.Name)
	}
	if !overwrite {
		if _, err := d.GetOAuth(s.Name); err == nil {
			return ErrExists
		}
	}
	return putOAuth(context.Background(), d.db, s)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func putOAuth(ctx context.Context, x execer, s OAuthSecret) error {
	if s.Tokens == nil {
		s.Tokens = map[string]string{}
	}
	if s.Meta == nil {
		s.Meta = map[string]string{}
	}
	tokens, err := json.Marshal(s.Tokens)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(s.Meta)
	if err != nil {
		return err
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	if s.RefreshedAt.IsZero() {
		s.RefreshedAt = time.Now()
	}
	_, err = x.ExecContext(ctx, `INSERT INTO oauth_secrets (`+oauthColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET provider = excluded.provider, tokens = excluded.tokens,
			meta = excluded.meta, expires_at = excluded.expires_at, refreshed_at = excluded.refreshed_at,
			failure = excluded.failure`,
		s.Name, s.Provider, string(tokens), string(meta), formatTime(s.ExpiresAt), formatTime(s.RefreshedAt), s.Failure, formatTime(s.CreatedAt))
	return err
}

func (d *DB) RefreshOAuth(ctx context.Context, name string, needed func(OAuthSecret) bool, refresh func(OAuthSecret) (OAuthSecret, error)) (OAuthSecret, bool, error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return OAuthSecret{}, false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return OAuthSecret{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	cur, err := scanOAuth(conn.QueryRowContext(ctx, `SELECT `+oauthColumns+` FROM oauth_secrets WHERE name = ?`, name))
	if err != nil {
		return OAuthSecret{}, false, err
	}
	if !needed(cur) {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return cur, false, err
		}
		committed = true
		return cur, false, nil
	}
	next, refreshErr := refresh(cur)
	if refreshErr != nil && next.Name == "" {
		return cur, false, refreshErr
	}
	next.Name, next.CreatedAt = cur.Name, cur.CreatedAt
	if err := putOAuth(ctx, conn, next); err != nil {
		return cur, false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return cur, false, err
	}
	committed = true
	return next, true, refreshErr
}
