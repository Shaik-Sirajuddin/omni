package message

import (
	"log/slog"

	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

var logger = pkglog.NewLoggerWithLevel("component", "store-message", slog.LevelError)
