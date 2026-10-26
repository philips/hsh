package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:8897", "listen address")
	logPath := flag.String("log", "", "file to append request lines to")
	flag.Parse()

	var mu sync.Mutex
	var logLine func(string)
	if *logPath == "" {
		logLine = func(string) {}
	} else {
		logLine = func(line string) {
			f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return
			}
			defer f.Close()
			fmt.Fprintln(f, line)
		}
	}

	access := "real-access-1"
	refresh := "real-refresh-1"
	issue := func(w http.ResponseWriter) {
		fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":%q,"expires_in":3600}`, access, refresh)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.PostForm.Get("grant_type") {
		case "password":
			logLine("token grant=password")
			w.Header().Set("Content-Type", "application/json")
			issue(w)
		case "refresh_token":
			got := r.PostForm.Get("refresh_token")
			logLine("token grant=refresh_token refresh=" + got)
			if got != refresh {
				http.Error(w, "invalid refresh token", http.StatusUnauthorized)
				return
			}
			refresh = "real-refresh-2"
			access = "real-access-2"
			w.Header().Set("Content-Type", "application/json")
			issue(w)
		default:
			http.Error(w, "unsupported grant type", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		want := "Bearer " + access
		mu.Unlock()
		got := r.Header.Get("Authorization")
		logLine("api auth=" + got)
		if got != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "api-ok")
	})

	fmt.Printf("fake oauth server listening on %s\n", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
