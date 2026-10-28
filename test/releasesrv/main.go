package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8898", "listen address")
	releaseDir := flag.String("releases", "", "directory serving fake releases: <dir>/<tag>/<asset>")
	metricsLog := flag.String("metrics-log", "", "file to append received metrics request bodies to")
	hitsLog := flag.String("hits-log", "", "file to append one line per /latest request to")
	flag.Parse()
	if *releaseDir == "" {
		log.Fatal("-releases is required")
	}

	var mu sync.Mutex
	appendLine := func(path, line string) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		if *hitsLog != "" {
			appendLine(*hitsLog, "hit")
		}
		tags := map[string]bool{}
		entries, err := os.ReadDir(*releaseDir)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
				tags[e.Name()] = true
			}
		}
		if len(tags) == 0 {
			http.Error(w, "no releases", 404)
			return
		}
		tag := "v0.0.0"
		for t := range tags {
			if t > tag {
				tag = t
			}
		}
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/tag/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "release page")
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/download/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		tag, asset := parts[0], parts[1]
		http.ServeFile(w, r, *releaseDir+"/"+tag+"/"+asset)
	})
	mux.HandleFunc("/hooks/metrics_report", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if *metricsLog != "" {
			appendLine(*metricsLog, string(body))
		}
		w.WriteHeader(200)
	})

	log.Printf("releasesrv listening on %s (releases in %s)", *addr, *releaseDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
