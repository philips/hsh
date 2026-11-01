package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

func jwtSegment(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

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

	antGen := 1
	antAccess := func() string { return fmt.Sprintf("sk-ant-oat01-real-%d", antGen) }
	antRefresh := func() string { return fmt.Sprintf("sk-ant-ort01-real-%d", antGen) }

	jwtHeader := jwtSegment(map[string]string{"alg": "RS256", "typ": "JWT"})
	jwtClaims := jwtSegment(map[string]any{"email": "user@example.com", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct-func"}})
	jwtAccess := jwtHeader + "." + jwtClaims + ".real-jwt-sig"

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
	mux.HandleFunc("POST /token-json", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		if err := json.Unmarshal(body, &req); err != nil {
			logLine("token-json not json: " + string(body))
			http.Error(w, "want a JSON body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req["grant_type"] {
		case "authorization_code":
			logLine("token-json grant=authorization_code")
		case "refresh_token":
			logLine("token-json grant=refresh_token refresh=" + req["refresh_token"])
			if req["refresh_token"] != antRefresh() {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			antGen++
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"unsupported_grant_type"}`)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, antAccess(), antRefresh())
	})
	mux.HandleFunc("POST /token-jwt", func(w http.ResponseWriter, r *http.Request) {
		logLine("token-jwt claims=" + jwtClaims)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"id_token":%q,"refresh_token":"real-jwt-refresh","expires_in":3600}`, jwtAccess, jwtAccess)
	})
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := map[string]bool{"Bearer " + access: true, "Bearer " + antAccess(): true, "Bearer " + jwtAccess: true}
		mu.Unlock()
		got := r.Header.Get("Authorization")
		logLine("api auth=" + got)
		if !ok[got] {
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
