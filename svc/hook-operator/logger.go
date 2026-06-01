package hookoperator

import (
	"log/slog"

	applog "github.com/Shaik-Sirajuddin/omni/pkg/log"
)

var logger *slog.Logger = applog.NewLogger("component", "hook-operator")
