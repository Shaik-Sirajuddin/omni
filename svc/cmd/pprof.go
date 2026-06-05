package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"

	"github.com/Shaik-Sirajuddin/memory/config"
)

// startPprof starts a pprof listener on 127.0.0.1:<OMNI_PPROF_PORT|6060>
// only when both dev.debug and dev.profiling are true in omni config.
// Returns a no-op shutdown func when either gate is false.
func startPprof() func(context.Context) error {
	resolver := &config.DefaultOmniConfigResolver{}
	cfg, err := resolver.GetUserSettings()
	if err != nil || cfg.Dev == nil || !cfg.Dev.Debug || !cfg.Dev.Profiling {
		return func(context.Context) error { return nil }
	}

	port := os.Getenv("OMNI_PPROF_PORT")
	if port == "" {
		port = "6060"
	}
	addr := fmt.Sprintf("127.0.0.1:%s", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go srv.ListenAndServe() //nolint:errcheck

	return srv.Shutdown
}
