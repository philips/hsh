package update

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
)

func TestReport(t *testing.T) {
	var mu sync.Mutex
	var gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		mu.Lock()
		gotContentType = r.Header.Get("Content-Type")
		json.Unmarshal(b, &gotBody)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Report(srv.URL, "v1.2.3", 38); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotContentType != "application/json" {
		t.Errorf("content type %q want application/json", gotContentType)
	}
	want := map[string]any{
		"product":                    "hsh",
		"version":                    "v1.2.3",
		"platform":                   runtime.GOOS + "/" + runtime.GOARCH,
		"sessions_since_last_update": float64(38),
	}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("field %q: got %v want %v", k, gotBody[k], v)
		}
	}
}

func TestReportServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := Report(srv.URL, "v1.2.3", 0); err == nil {
		t.Fatal("expected an error on a 500 response")
	}
}
