package otel

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	gotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// Config describes the service being instrumented.
// All fields are optional — sensible defaults are applied for anything left empty.
type Config struct {
	// ServiceName is the OTel service.name resource attribute.
	// Defaults to the last path segment of the module path (e.g. "alchemist").
	ServiceName string

	// ServiceNamespace is the OTel service.namespace resource attribute.
	// Use this to group services belonging to the same system or team.
	// Defaults to the penultimate path segment of the module path (e.g. "bitsmithy").
	ServiceNamespace string

	// ServiceVersion is the OTel service.version resource attribute.
	// Defaults to the Go module version from build info, or "unknown".
	ServiceVersion string

	// Endpoint is the full URL of the OTLP collector, e.g. "http://localhost:4318".
	// When set it is passed as WithEndpointURL to all three exporters (traces, metrics, logs).
	// When empty the standard OTel environment variables are used as a fallback
	// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, etc.).
	Endpoint string

	// LogLevel controls the minimum level written to stderr.
	// Defaults to slog.LevelInfo.
	LogLevel slog.Level
}

// Setup initialises the three OTel pillars (traces, metrics, logs), sets the
// global providers, and returns a shutdown func, a pre-wired *slog.Logger,
// and the tracer and meter for the named service.
//
// OTLP endpoint and headers are read from the standard OTel environment
// variables (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS, etc.).
//
// Typical usage:
//
//	shutdown, log, tracer, meter, err := telemetry.Setup(ctx, telemetry.Config{})
//	if err != nil { ... }
//	defer shutdown(ctx)
func Setup(ctx context.Context, cfg Config) (
	shutdown func(context.Context) error,
	log *slog.Logger,
	tracer trace.Tracer,
	meter metric.Meter,
	err error,
) {
	if cfg.ServiceName == "" || cfg.ServiceNamespace == "" {
		name, ns := binaryName()
		if cfg.ServiceName == "" {
			cfg.ServiceName = name
		}
		if cfg.ServiceNamespace == "" {
			cfg.ServiceNamespace = ns
		}
	}
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = moduleVersion()
	}

	var shutdownFuncs []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if e := fn(ctx); e != nil {
				errs = append(errs, e)
			}
		}
		return errors.Join(errs...)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceNamespace(cfg.ServiceNamespace),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return shutdown, nil, nil, nil, err
	}

	// Traces
	var traceOpts []otlptracehttp.Option
	if cfg.Endpoint != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	}
	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return shutdown, nil, nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)
	gotel.SetTracerProvider(tp)
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	// Metrics
	var metricOpts []otlpmetrichttp.Option
	if cfg.Endpoint != "" {
		metricOpts = append(metricOpts, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
	}
	metricExp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		return shutdown, nil, nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second),
		)),
		sdkmetric.WithResource(res),
	)
	gotel.SetMeterProvider(mp)
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	// Propagator
	gotel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Logs
	var logOpts []otlploghttp.Option
	if cfg.Endpoint != "" {
		logOpts = append(logOpts, otlploghttp.WithEndpointURL(cfg.Endpoint))
	}
	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		return shutdown, nil, nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp,
			sdklog.WithExportTimeout(5*time.Second),
		)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)
	shutdownFuncs = append(shutdownFuncs, lp.Shutdown)

	log = NewLogger(cfg.ServiceName, cfg.LogLevel)
	tracer = gotel.Tracer(cfg.ServiceName)
	meter = gotel.Meter(cfg.ServiceName)
	return shutdown, log, tracer, meter, nil
}

// NewLogger builds a *slog.Logger that fans out to the OTel log pipeline
// and stderr JSON. The stderr handler is wrapped with TraceHandler so
// trace_id and span_id are automatically injected into every record.
//
// Call this after Setup if you need an additional logger instance, or use
// the one returned by Setup directly.
func NewLogger(serviceName string, level slog.Level) *slog.Logger {
	otelHandler := otelslog.NewHandler(serviceName)
	stderrHandler := &TraceHandler{
		Handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}),
	}
	return slog.New(FanoutHandler{otelHandler, stderrHandler})
}

// DetachedContext returns a fresh context that is never cancelled but carries
// the active span from ctx.
//
// Use this when emitting telemetry after an operation that may have timed out:
// a cancelled ctx causes the OTel SDK to silently drop log records and metric
// data points.
func DetachedContext(ctx context.Context) context.Context {
	return trace.ContextWithSpan(context.Background(), trace.SpanFromContext(ctx))
}

func moduleVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "unknown"
}

// binaryName returns the service name and namespace derived from the module path.
// "github.com/bitsmithy/alchemist" → name="alchemist", namespace="bitsmithy"
// These map to OTel's service.name and service.namespace resource attributes.
func binaryName() (name, namespace string) {
	if info, ok := debug.ReadBuildInfo(); ok && info.Path != "" {
		dir, base := path.Split(strings.TrimRight(info.Path, "/"))
		parent := path.Base(strings.TrimRight(dir, "/"))
		if parent == "." || parent == "" {
			return base, "unknown"
		}
		return base, parent
	}
	return "unknown", "unknown"
}
