// Package log provides a shared structured logger factory used across all
// memory modules. The log level is resolved at construction time:
//
//  1. DEV env var set (any non-empty value) → Debug
//  2. ~/.config/omni/config.json has dev.debug == true → Debug
//  3. Otherwise → Info
//
// No internal module dependencies — safe to import from any module.
package log

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// NewLogger returns a structured logger tagged with the given key/value pair.
// Writes to the base sink (stderr by default, or OMNI_LOG_FILE if set); also fans
// out to any OTLP targets registered via InitOtel.
func NewLogger(key, component string) *slog.Logger {
	level := resolveLevel()
	opts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	handlers := append([]slog.Handler{slog.NewTextHandler(logSink(), opts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
}

// NewLoggerWithLevel is like NewLogger but forces a minimum log level regardless of env/config.
func NewLoggerWithLevel(key, component string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	handlers := append([]slog.Handler{slog.NewTextHandler(logSink(), opts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
}

var (
	sinkOnce sync.Once
	sink     io.Writer
)

// logSink returns the base log destination. Default is stderr (captured by
// journald for the systemd-run daemon). When OMNI_LOG_FILE is set, logs are
// appended to that file instead — so interactive clients (e.g. `omni agent
// attach`) never render debug output over the user's terminal. Resolved once.
func logSink() io.Writer {
	sinkOnce.Do(func() {
		sink = os.Stderr
		if path := strings.TrimSpace(os.Getenv("OMNI_LOG_FILE")); path != "" {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				sink = f
			}
		}
	})
	return sink
}

func resolveLevel() slog.Level {
	if os.Getenv("DEV") != "" {
		return slog.LevelDebug
	}
	if debug, err := readOmniDebug(); err == nil && debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func readOmniDebug() (bool, error) {
	dir, err := xdgConfigHome()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "omni", "config.json"))
	if err != nil {
		return false, err
	}
	var cfg struct {
		Dev *struct {
			Debug bool `json:"debug"`
		} `json:"dev,omitempty"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, err
	}
	return cfg.Dev != nil && cfg.Dev.Debug, nil
}

func xdgConfigHome() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}
