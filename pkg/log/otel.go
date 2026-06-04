package log

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/sdk/log"
)

// OtelTarget describes a single OTLP log destination.
type OtelTarget struct {
	Endpoint string
	Headers  map[string]string
}

// EnvTarget reads OTEL_EXPORTER_OTLP_ENDPOINT from the environment.
// Returns a zero OtelTarget (skipped by InitOtel) if the variable is unset.
func EnvTarget() OtelTarget {
	return OtelTarget{Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")}
}

var (
	otelMu       sync.RWMutex
	otelHandlers []slog.Handler
)

// InitOtel registers one or more OTLP log targets. Safe to call multiple times.
// Targets with an empty Endpoint are silently skipped.
func InitOtel(targets ...OtelTarget) {
	otelMu.Lock()
	defer otelMu.Unlock()
	for _, t := range targets {
		if t.Endpoint == "" {
			continue
		}
		if h := buildOTLPHandler(t); h != nil {
			otelHandlers = append(otelHandlers, h)
		}
	}
}

func buildOTLPHandler(t OtelTarget) slog.Handler {
	opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(t.Endpoint)}
	if len(t.Headers) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(t.Headers))
	}
	exp, err := otlploghttp.New(context.Background(), opts...)
	if err != nil {
		return nil
	}
	provider := otellog.NewLoggerProvider(
		otellog.WithProcessor(otellog.NewBatchProcessor(exp)),
	)
	return otelslog.NewHandler("omni", otelslog.WithLoggerProvider(provider))
}

func activeHandlers() []slog.Handler {
	otelMu.RLock()
	defer otelMu.RUnlock()
	out := make([]slog.Handler, len(otelHandlers))
	copy(out, otelHandlers)
	return out
}

// multiHandler fans out log records to all contained handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return multiHandler{hs}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return multiHandler{hs}
}
