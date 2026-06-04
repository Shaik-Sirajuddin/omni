package main

import (
	"github.com/Shaik-Sirajuddin/memory/config"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

// initOtel registers OTLP log targets from two sources:
//  1. env — OTEL_EXPORTER_OTLP_ENDPOINT (zero-config default)
//  2. user config — OmniConfig.Otel in ~/.config/omni/config.json
func initOtel() {
	pkglog.InitOtel(pkglog.EnvTarget())

	resolver := &config.DefaultOmniConfigResolver{}
	cfg, err := resolver.GetUserSettings()
	if err != nil || cfg.Otel == nil || cfg.Otel.Endpoint == nil {
		return
	}
	pkglog.InitOtel(pkglog.OtelTarget{
		Endpoint: *cfg.Otel.Endpoint,
		Headers:  cfg.Otel.Headers,
	})
}
