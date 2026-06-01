// Package log provides a shared logger constructor for sandbox providers.
package log

import (
	"log/slog"

	applog "github.com/Shaik-Sirajuddin/omni/pkg/log"
)

// NewLogger returns a sandbox-tagged logger.
// Log level is resolved by the shared application logger from OmniConfig.
func NewLogger(provider string) *slog.Logger {
	return applog.NewLogger("sandbox", provider)
}
