// Package log provides a shared structured logger factory used across all
// memory modules. The log level is resolved at construction time:
//
//  1. OMNI_DEBUG env var set → "1"/"true"/"on" forces debug; "0"/"false"/"off" forces info
//  2. DEV env var set (any non-empty value) → Debug
//  3. ~/.config/omni/config.json has dev.debug == true → Debug
//  4. Otherwise → Info
//
// Log destination is resolved lazily on the first write, so process-level
// configuration set in main() is respected even for package-level loggers.
// Priority (evaluated at first write):
//
//  1. UseStderrForAll() called → stderr (all loggers in the process)
//  2. WithStderr() option → stderr (this logger only)
//  3. OMNI_LOG_FILE env var set → that file, all levels
//  4. Debug mode → ~/.omni/debug/<component>.log
//  5. Otherwise → ~/.omni/log/omni.log  (silent; no terminal output for CLI users)
//
// OTLP targets registered via InitOtel() always receive records regardless
// of the text destination above.
//
// Daemon startup: call UseStderrForAll() before the first log write so every
// sub-package logger lands in journald without needing WithStderr() everywhere.
//
// CLI with session: call InitSessionLog() to set OMNI_LOG_FILE to
// ~/.omni/log/session-<pid>.log so all in-process components share one file.
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

// WithStderr directs this logger's text output to stderr regardless of other
// settings. Use for per-logger overrides; prefer UseStderrForAll() for daemons.
func WithStderr() Option {
	return func(o *logOptions) { o.useStderr = true }
}

var (
	processWriterMu sync.RWMutex
	processWriter   io.Writer // nil = use per-logger resolution
)

// UseStderrForAll forces every logger in this process to write to stderr.
// Call once at daemon startup before any log message is written so that
// sub-package loggers (constructed at package init) also land in journald.
//
// Limitation: a logger that emits a message during package init() (before
// main() runs) will have already resolved its destination via once.Do and
// will not be redirected. In practice this is rare — most package-level
// loggers are constructed but not written to during init.
func UseStderrForAll() {
	processWriterMu.Lock()
	processWriter = os.Stderr
	processWriterMu.Unlock()
}

// InitSessionLog sets OMNI_LOG_FILE to ~/.omni/log/session-<pid>.log
// if it is not already set. Call for commands that attach to an agent session
// so that all in-process components and child subprocesses share one file.
// Long-lived daemon processes should NOT call this — use UseStderrForAll().
func InitSessionLog() {
	if os.Getenv("OMNI_LOG_FILE") != "" {
		return
	}
	home, err := omniHome()
	if err != nil {
		return
	}
	logDir := filepath.Join(home, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(logDir, fmt.Sprintf("session-%d.log", os.Getpid()))
	_ = os.Setenv("OMNI_LOG_FILE", path)
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
	lw := &lazyWriter{component: component, level: level, useStderr: o.useStderr}
	handlerOpts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	// OTEL handlers are always appended regardless of text destination.
	handlers := append([]slog.Handler{slog.NewTextHandler(lw, handlerOpts)}, activeHandlers()...)
	return slog.New(multiHandler{handlers}).With(key, component)
}

// lazyWriter defers destination resolution to the first Write call so that
// process-level config (UseStderrForAll, InitSessionLog) set in main() is
// respected even for package-level loggers constructed before main() runs.
type lazyWriter struct {
	once      sync.Once
	component string
	level     slog.Level
	useStderr bool
	w         io.Writer
}

func (lw *lazyWriter) Write(p []byte) (int, error) {
	lw.once.Do(func() {
		lw.w = resolveWriterNow(lw.component, lw.level, lw.useStderr)
	})
	return lw.w.Write(p)
}

func resolveWriterNow(component string, level slog.Level, useStderr bool) io.Writer {
	processWriterMu.RLock()
	pw := processWriter
	processWriterMu.RUnlock()
	if pw != nil {
		return pw
	}
	if useStderr {
		return os.Stderr
	}
	path := os.Getenv("OMNI_LOG_FILE")
	if path == "" {
		home, err := omniHome()
		if err != nil {
			// No home dir — fall back to stderr rather than polluting /tmp.
			return os.Stderr
		}
		if level <= slog.LevelDebug {
			// Debug mode: per-component file for easy filtering.
			debugDir := filepath.Join(home, "debug")
			_ = os.MkdirAll(debugDir, 0o755)
			path = filepath.Join(debugDir, sanitizeComponent(component)+".log")
		} else {
			// Info mode: shared persistent log — silent to the terminal.
			logDir := filepath.Join(home, "log")
			_ = os.MkdirAll(logDir, 0o755)
			path = filepath.Join(logDir, "omni.log")
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Can't open the log file — fall back to stderr silently.
		return os.Stderr
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

// omniHome returns ~/.omni (app data dir, distinct from XDG config dir).
func omniHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".omni"), nil
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
