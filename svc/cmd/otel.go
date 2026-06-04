package main

import (
	"context"

	"github.com/Shaik-Sirajuddin/memory/config"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

// initOtel registers OTLP log targets from two sources:
//  1. env — OTEL_EXPORTER_OTLP_ENDPOINT (zero-config default)
//  2. user config — OmniConfig.Otel in ~/.config/omni/config.json
//
// It also starts the metrics pipeline (Go runtime/GC/memory → OTLP) and
// returns a flush function to call on SIGTERM.
func initOtel() (shutdownMetrics func(context.Context) error) {
	pkglog.Version = Version

	pkglog.InitOtel(pkglog.EnvTarget())

	resolver := &config.DefaultOmniConfigResolver{}
	cfg, err := resolver.GetUserSettings()
	if err != nil || cfg.Otel == nil || cfg.Otel.Endpoint == nil {
		return pkglog.InitOtelMetrics()
	}

	userTarget := pkglog.OtelTarget{
		Endpoint: *cfg.Otel.Endpoint,
		Headers:  cfg.Otel.Headers,
	}
	pkglog.InitOtel(userTarget)
	return pkglog.InitOtelMetrics(userTarget)
}
