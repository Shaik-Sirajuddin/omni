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
	"log/slog"
	"os"
	"path/filepath"
)

// NewLogger returns a structured logger tagged with the given key/value pair.
// Writes to stderr always; also fans out to any OTLP targets registered via InitOtel.
func NewLogger(key, component string) *slog.Logger {
	level := resolveLevel()
	opts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	handlers := append([]slog.Handler{slog.NewTextHandler(os.Stderr, opts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
}

// NewLoggerWithLevel is like NewLogger but forces a minimum log level regardless of env/config.
func NewLoggerWithLevel(key, component string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	handlers := append([]slog.Handler{slog.NewTextHandler(os.Stderr, opts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
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
