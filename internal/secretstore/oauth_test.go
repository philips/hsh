package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.db")
	db, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func testLogin(name string) OAuthSecret {
	return OAuthSecret{
		Name:      name,
		Provider:  "anthropic",
		Tokens:    map[string]string{"access_token": "real-access", "refresh_token": "real-refresh"},
		Meta:      map[string]string{"scope": "user:inference"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestOAuthSecretRoundTrip(t *testing.T) {
	db, _ := openTestStore(t)
	if err := db.PutOAuth(testLogin("claude"), false); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetOAuth("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "anthropic" || got.Tokens["refresh_token"] != "real-refresh" || got.Meta["scope"] != "user:inference" {
		t.Fatalf("got %+v", got)
	}
	if err := db.PutOAuth(testLogin("claude"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("a second put without overwrite: %v", err)
	}
	if err := db.Set("claude", "static", true); err == nil || !strings.Contains(err.Error(), "OAuth login") {
		t.Fatalf("a static secret must not shadow an OAuth login: %v", err)
	}
	if err := db.Set("key", "v", false); err != nil {
		t.Fatal(err)
	}
	if err := db.PutOAuth(testLogin("key"), true); err == nil || !strings.Contains(err.Error(), "static secret") {
		t.Fatalf("an OAuth login must not shadow a static secret: %v", err)
	}
	entries, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "claude" || entries[0].Kind != "oauth" || entries[0].Provider != "anthropic" || entries[1].Kind != "static" {
		t.Fatalf("list = %+v", entries)
	}
	if _, err := db.Get("claude"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get must not return OAuth logins: %v", err)
	}
	if err := db.Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOAuth("claude"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestOAuthMigrationKeepsStaticSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE secrets (name TEXT PRIMARY KEY, value TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO secrets VALUES ('old', 'kept', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z');
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, _, err := Open(path)
	if err != nil {
		t.Fatalf("a version 1 store must migrate: %v", err)
	}
	defer db.Close()
	if v, err := db.Get("old"); err != nil || v != "kept" {
		t.Fatalf("static secret after migration: %q %v", v, err)
	}
	if err := db.PutOAuth(testLogin("new"), false); err != nil {
		t.Fatalf("the migrated store has no oauth table: %v", err)
	}
}

func TestRefreshOAuthSerializesRefreshers(t *testing.T) {
	db, path := openTestStore(t)
	login := testLogin("claude")
	login.ExpiresAt = time.Now().Add(-time.Minute)
	if err := db.PutOAuth(login, false); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	needed := func(s OAuthSecret) bool { return time.Until(s.ExpiresAt) < 5*time.Minute }
	refresh := func(s OAuthSecret) (OAuthSecret, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		s.Tokens = map[string]string{"access_token": "real-access-2", "refresh_token": "real-refresh-2"}
		s.ExpiresAt = time.Now().Add(time.Hour)
		s.RefreshedAt = time.Now()
		return s, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, _, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer other.Close()
			if _, _, err := other.RefreshOAuth(context.Background(), "claude", needed, refresh); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("refresh ran %d times; the refresh token must be spent once", n)
	}
	got, _ := db.GetOAuth("claude")
	if got.Tokens["refresh_token"] != "real-refresh-2" || got.CreatedAt.IsZero() {
		t.Fatalf("after refresh: %+v", got)
	}
}

func TestRefreshOAuthRecordsFailure(t *testing.T) {
	db, _ := openTestStore(t)
	if err := db.PutOAuth(testLogin("claude"), false); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("invalid_grant")
	_, changed, err := db.RefreshOAuth(context.Background(), "claude", func(OAuthSecret) bool { return true }, func(s OAuthSecret) (OAuthSecret, error) {
		s.Failure = "invalid_grant"
		return s, boom
	})
	if !changed || !errors.Is(err, boom) {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := db.GetOAuth("claude")
	if got.Failure != "invalid_grant" {
		t.Fatalf("failure not stored: %+v", got)
	}
	_, changed, err = db.RefreshOAuth(context.Background(), "claude", func(OAuthSecret) bool { return true }, func(s OAuthSecret) (OAuthSecret, error) {
		return OAuthSecret{}, errors.New("network down")
	})
	if changed || err == nil {
		t.Fatalf("a transient error must leave the row alone: changed=%v err=%v", changed, err)
	}
}
