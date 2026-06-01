// Package log provides a shared logger constructor for codeagent connectors.
package log

import (
	"log/slog"

	applog "github.com/Shaik-Sirajuddin/omni/pkg/log"
)

// NewLogger returns a connector-tagged logger.
// Log level is resolved by the shared application logger from OmniConfig.
func NewLogger(connector string) *slog.Logger {
	return applog.NewLogger("connector", connector)
}
