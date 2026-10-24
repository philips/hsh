package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8899", "listen address")
	logPath := flag.String("log", "", "file to append request header lines to")
	content := flag.String("content", "hello world", "assistant reply content")
	id := flag.String("id", "", "instance id served at /id and as the X-Dummy-Id header")
	flag.Parse()

	var mu sync.Mutex
	logLine := func(line string) {
		fmt.Fprintln(os.Stderr, line)
		if *logPath == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintln(f, line)
		f.Close()
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, *id)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var headers []string
		for _, k := range []string{"Authorization", "Api-Key", "User-Agent", "Content-Type"} {
			if v := r.Header.Get(k); v != "" {
				headers = append(headers, k+": "+v)
			}
		}
		logLine(fmt.Sprintf("%s %s | %s", r.Method, r.URL.Path, strings.Join(headers, " | ")))
		w.Header().Set("X-Dummy-Id", *id)
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		model := req.Model
		if model == "" {
			model = "dummy-model"
		}
		created := time.Now().Unix()
		half := len(*content) / 2
		first, second := (*content)[:half], (*content)[half:]
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			send := func(format string, args ...any) {
				fmt.Fprintf(w, "data: "+format+"\n\n", args...)
				if flusher != nil {
					flusher.Flush()
				}
			}
			send(`{"id":"chatcmpl-dummy","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":%q},"finish_reason":null}]}`, created, model, first)
			send(`{"id":"chatcmpl-dummy","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, created, model, second)
			send(`{"id":"chatcmpl-dummy","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, created, model)
			send(`{"id":"chatcmpl-dummy","object":"chat.completion.chunk","created":%d,"model":%q,"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, created, model)
			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-dummy",
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []any{
				map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": *content},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		var headers []string
		for _, k := range []string{"Authorization", "Api-Key", "User-Agent"} {
			if v := r.Header.Get(k); v != "" {
				headers = append(headers, k+": "+v)
			}
		}
		logLine(fmt.Sprintf("%s %s | %s", r.Method, r.URL.Path, strings.Join(headers, " | ")))
		w.Header().Set("X-Dummy-Id", *id)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "dummy-model", "object": "model", "created": time.Now().Unix(), "owned_by": "dummy"},
			},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logLine(fmt.Sprintf("%s %s | 404", r.Method, r.URL.Path))
		http.Error(w, `{"error":{"message":"not found","type":"invalid_request_error"}}`, http.StatusNotFound)
	})

	fmt.Fprintf(os.Stderr, "dummy openai-compatible endpoint listening on %s\n", *addr)
	_ = http.ListenAndServe(*addr, mux)
}
