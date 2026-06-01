package cli

import (
	"bytes"
	"io"

	"github.com/Shaik-Sirajuddin/omni/cli/theme"
)

// logWriter wraps an io.Writer and colorises [LEVEL] prefixes using the active theme.
type logWriter struct{ w io.Writer }

// newLogWriter returns a writer that applies theme colours to log-level tags.
func newLogWriter(w io.Writer) io.Writer { return &logWriter{w: w} }

var levelRoles = map[string]theme.Role{
	"[DEBUG]": theme.RoleLogDebug,
	"[INFO]":  theme.RoleLogInfo,
	"[WARN]":  theme.RoleLogWarn,
	"[ERROR]": theme.RoleLogError,
	"[FATAL]": theme.RoleLogFatal,
}

func (lw *logWriter) Write(p []byte) (int, error) {
	line := p
	t := theme.Active()
	for tag, role := range levelRoles {
		tagB := []byte(tag)
		if bytes.HasPrefix(line, tagB) {
			colored := []byte(t.Apply(role, tag))
			out := append(colored, line[len(tagB):]...)
			_, err := lw.w.Write(out)
			return len(p), err
		}
	}
	return lw.w.Write(p)
}
