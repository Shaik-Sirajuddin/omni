package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type hookResponse struct {
	Continue       bool    `json:"continue"`
	SuppressOutput bool    `json:"suppress_output"`
	StopReason     *string `json:"stop_reason,omitempty"`
	SystemMessage  *string `json:"system_message,omitempty"`
}

type requestRecord struct {
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	EventName string          `json:"event_name"`
	Body      json.RawMessage `json:"body,omitempty"`
	Received  time.Time       `json:"received"`
}

type requestStore struct {
	mu      sync.RWMutex
	records map[string]requestRecord
}

func newRequestStore() *requestStore {
	return &requestStore{records: map[string]requestRecord{}}
}

func (s *requestStore) set(name string, record requestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[name] = record
}

func (s *requestStore) all() map[string]requestRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]requestRecord, len(s.records))
	for k, v := range s.records {
		out[k] = v
	}
	return out
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	host := envOr("HOOK_OPERATOR_TEST_CLIENT_HOST", "127.0.0.1")
	port := envOr("HOOK_OPERATOR_TEST_CLIENT_PORT", "18081")
	addr := net.JoinHostPort(host, port)

	store := newRequestStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/hooks/", handleHook(logger, store))
	mux.HandleFunc("/hooks", handleHook(logger, store))
	mux.HandleFunc("/requests", handleRequests(store))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("hook operator test client listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("hook operator test client stopped", "err", err)
		os.Exit(1)
	}
}

func handleHook(logger *slog.Logger, store *requestStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/hooks")
		name = strings.Trim(name, "/")
		if name == "" {
			name = "default"
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
			return
		}

		record := requestRecord{
			Method:    r.Method,
			Path:      r.URL.Path,
			EventName: r.Header.Get("X-Hook-Event"),
			Body:      normalizeJSON(body),
			Received:  time.Now().UTC(),
		}
		store.set(name, record)

		logger.Info(
			"hook payload received",
			"name", name,
			"event", record.EventName,
			"path", record.Path,
			"payload", string(record.Body),
		)

		writeJSON(w, hookResponse{Continue: true, SuppressOutput: false})
	}
}

func handleRequests(store *requestStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, store.all())
	}
}

func normalizeJSON(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}

	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return json.RawMessage(jsonQuote(string(body)))
	}

	normalized, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(jsonQuote(string(body)))
	}
	return normalized
}

func jsonQuote(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
