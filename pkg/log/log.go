// Package log provides a shared structured logger factory used across all
// memory modules. The log level is resolved at construction time:
//
//  1. OMNI_DEBUG env var set → "1"/"true"/"on" forces debug; "0"/"false"/"off" forces info
//  2. DEV env var set (any non-empty value) → Debug
//  3. ~/.config/omni/config.json has dev.debug == true → Debug
//  4. Otherwise → Info
//
// Log destination (in priority order):
//  1. WithStderr() option → stderr always
//  2. OMNI_LOG_FILE env var set → that file, all levels
//  3. Debug mode, no OMNI_LOG_FILE → ~/.config/omni/debug/<component>.log
//  4. Otherwise → stderr
//
// Call InitSessionLog() at process startup to auto-set OMNI_LOG_FILE to
// ~/.config/omni/log/session-<pid>.log so all in-process components and
// child subprocesses share one file for the session.
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

// WithStderr directs all log output to stderr regardless of other settings.
// Use this for systemd services whose stdout/stderr is captured by journald.
func WithStderr() Option {
	return func(o *logOptions) { o.useStderr = true }
}

// InitSessionLog sets OMNI_LOG_FILE to ~/.config/omni/log/session-<pid>.log
// if it is not already set. Call this once at process startup (before any
// logger is constructed) so that the CLI, operator, code agents, and MCP
// stdio subprocesses all write to the same file for the session.
// Long-lived daemon processes should NOT call this.
func InitSessionLog() {
	if os.Getenv("OMNI_LOG_FILE") != "" {
		return
	}
	cfgHome, err := xdgConfigHome()
	if err != nil {
		return
	}
	logDir := filepath.Join(cfgHome, "omni", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(logDir, fmt.Sprintf("session-%d.log", os.Getpid()))
	if err := os.Setenv("OMNI_LOG_FILE", path); err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "omni: session log → %s\n", path)
}

// NewLogger returns a structured logger tagged with the given key/value pair.
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

func resolveWriter(component string, level slog.Level, o *logOptions) io.Writer {
	if o.useStderr {
		return os.Stderr
	}
	path := os.Getenv("OMNI_LOG_FILE")
	if path == "" {
		// No session file — fall back to auto debug file or stderr.
		if level > slog.LevelDebug {
			return os.Stderr
		}
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
		fmt.Fprintf(os.Stderr, "omni: failed to open log %s: %v; falling back to stderr\n", path, err)
		return os.Stderr
	}
	if _, loaded := announcedPaths.LoadOrStore(path, struct{}{}); !loaded {
		fmt.Fprintf(os.Stderr, "omni: log → %s\n", path)
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

// resolveLevel determines the log level for this process.
// OMNI_DEBUG overrides all other sources for per-session control.
func resolveLevel() slog.Level {
	if v := os.Getenv("OMNI_DEBUG"); v != "" {
		switch v {
		case "0", "false", "off":
			return slog.LevelInfo
		default:
			return slog.LevelDebug
		}
	}
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
