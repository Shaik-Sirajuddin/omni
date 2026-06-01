package impl

import (
	"log/slog"

	applog "github.com/Shaik-Sirajuddin/omni/pkg/log"
)

var logger = newLogger()

func newLogger() *slog.Logger {
	return applog.NewLogger("component", "operator")
}
