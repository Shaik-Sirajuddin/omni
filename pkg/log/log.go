// Package log provides a shared structured logger factory used across all
// memory modules. The log level is resolved at construction time:
//
//  1. DEV env var set (any non-empty value) → Debug
//  2. ~/.config/omni/config.json has dev.debug == true → Debug
//  3. Otherwise → Info
//
// In debug mode, output goes to a file in the OS temp directory
// (e.g. /tmp/omni-debug-<component>.log). Set OMNI_LOG_FILE to override
// the path. Pass WithStderr() to write to stderr instead — use this for
// systemd services whose stdout/stderr is captured by journald.
//
// No internal module dependencies — safe to import from any module.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Option configures Logger construction.
type Option func(*logOptions)

type logOptions struct {
	useStderr bool
}

// WithStderr directs all log output to stderr regardless of debug mode.
// Use this for systemd services whose stdout/stderr is captured by journald.
func WithStderr() Option {
	return func(o *logOptions) { o.useStderr = true }
}

// NewLogger returns a structured logger tagged with the given key/value pair.
// In debug mode, output goes to a tmp file unless WithStderr() is passed.
func NewLogger(key, component string, opts ...Option) *slog.Logger {
	level := resolveLevel()
	return buildLogger(key, component, level, opts)
}

// NewLoggerWithLevel is like NewLogger but forces a minimum log level regardless of env/config.
func NewLoggerWithLevel(key, component string, level slog.Level, opts ...Option) *slog.Logger {
	return buildLogger(key, component, level, opts)
}

func buildLogger(key, component string, level slog.Level, opts []Option) *slog.Logger {
	o := &logOptions{}
	for _, opt := range opts {
		opt(o)
	}
	w := resolveWriter(component, level, o)
	handlerOpts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	handlers := append([]slog.Handler{slog.NewTextHandler(w, handlerOpts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
}

var announcedPaths sync.Map

// resolveWriter picks the log destination.
// Debug + no WithStderr → file (OMNI_LOG_FILE or ~/.config/omni/debug/<component>.log).
// Prints the path to stderr the first time a given file is opened.
func resolveWriter(component string, level slog.Level, o *logOptions) io.Writer {
	if o.useStderr || level > slog.LevelDebug {
		return os.Stderr
	}
	path := os.Getenv("OMNI_LOG_FILE")
	if path == "" {
		if dir, err := xdgConfigHome(); err == nil {
			debugDir := filepath.Join(dir, "omni", "debug")
			_ = os.MkdirAll(debugDir, 0o755)
			path = filepath.Join(debugDir, sanitizeComponent(component)+".log")
		} else {
			path = filepath.Join(os.TempDir(), "omni-debug-"+sanitizeComponent(component)+".log")
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "omni: failed to open debug log %s: %v; falling back to stderr\n", path, err)
		return os.Stderr
	}
	if _, loaded := announcedPaths.LoadOrStore(path, struct{}{}); !loaded {
		fmt.Fprintf(os.Stderr, "omni: debug log → %s\n", path)
	}
	return f
}

func sanitizeComponent(s string) string {
	out := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c == '/' || c == '\\' || c == ' ' {
			c = '-'
		}
		out[i] = c
	}
	return string(out)
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
