package log

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	goruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otellog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// Version is embedded in the OTel resource as service.version. The calling
// binary should set this at startup (e.g. pkglog.Version = buildVersion).
var Version = "dev"

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

// metricsEndpoint resolves the OTLP metrics endpoint in priority order:
//  1. OTEL_EXPORTER_OTLP_METRICS_ENDPOINT (signal-specific)
//  2. OTEL_EXPORTER_OTLP_ENDPOINT (shared with logs)
//  3. http://localhost:4318 (standard local/internal default)
func metricsEndpoint() string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); v != "" {
		return v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		return v
	}
	return "http://localhost:4318"
}

// InitOtelMetrics starts the OTLP metrics pipeline and Go runtime instrumentation.
// It resolves the endpoint automatically (see metricsEndpoint) so no flags are
// required; set OMNI_OTEL_METRICS=off to disable entirely.
// The returned func flushes and shuts down the MeterProvider — call it on SIGTERM.
func InitOtelMetrics(targets ...OtelTarget) (shutdown func(context.Context) error) {
	if strings.EqualFold(os.Getenv("OMNI_OTEL_METRICS"), "off") {
		return func(context.Context) error { return nil }
	}

	endpoint := metricsEndpoint()

	// Override endpoint from the first non-empty explicit target, if provided.
	for _, t := range targets {
		if t.Endpoint != "" {
			endpoint = t.Endpoint
			break
		}
	}

	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(endpoint)}
	for _, t := range targets {
		if t.Endpoint != "" && len(t.Headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(t.Headers))
			break
		}
	}

	exp, err := otlpmetrichttp.New(context.Background(), opts...)
	if err != nil {
		return func(context.Context) error { return nil }
	}

	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("omni-svc"),
			semconv.ServiceVersion(Version),
		),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Route OTel internal errors (e.g. per-tick "connection refused" when no
	// collector is reachable) through slog at DEBUG instead of the default
	// stdlib log.Println, which bypasses slog and spams journalctl.
	otelErrLogger := NewLogger("component", "otel")
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		otelErrLogger.Debug("otel internal error", "err", err)
	}))

	// runtime.Start registers Go runtime/GC/memory metrics using runtime/metrics
	// (not the legacy ReadMemStats stop-the-world path). Errors are non-fatal.
	_ = goruntime.Start(goruntime.WithMeterProvider(mp))

	return mp.Shutdown
}
