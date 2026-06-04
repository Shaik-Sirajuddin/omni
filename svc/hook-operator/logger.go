package hookoperator

import (
	"log/slog"

	applog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

var logger *slog.Logger = applog.NewLogger("component", "hook-operator", applog.WithStderr())
