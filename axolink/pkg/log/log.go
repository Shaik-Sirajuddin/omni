// Package log provides component-scoped loggers for axolink packages, wrapping
// the shared github.com/Shaik-Sirajuddin/memory/pkg/log implementation so engine,
// cli, and other axolink components share one logging setup.
package log

import (
	"log/slog"

	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

// New returns a logger tagged with the given component name.
func New(component string) *slog.Logger {
	return pkglog.NewLogger("component", component)
}
