# go-otel

A thin, opinionated OpenTelemetry setup library for Go services. One call wires up traces, metrics, and logs over OTLP/HTTP, returns a pre-configured `*slog.Logger`, and exposes HTTP middleware for `http.ServeMux`.

- **Single-call setup** — `telemetry.Setup(ctx, config)`, no builder pattern
- **All config optional** — sensible defaults derived from build info and environment
- **OTLP/HTTP only** — opinionated; no stdout/Zipkin/Jaeger in the setup path
- **Env var fallback** — standard `OTEL_*` env vars apply when `Config.Endpoint` is empty
- **Ergonomic wrappers** — `Trace()` for spans, `Counter`/`Histogram`/`Gauge`/`UpDownCounter` for metrics
- **Test harness** — `oteltest.Setup(t)` gives you in-memory providers with zero boilerplate

## Requirements

- Go 1.24 or later
- An OTLP-compatible collector (e.g. [Grafana Alloy](https://grafana.com/docs/alloy/), [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/))

## Installation

```sh
go get github.com/bitsmithy/go-otel
```

## Quick start

```go
import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    telemetry "github.com/bitsmithy/go-otel"
)

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    shutdown, log, tracer, meter, err := telemetry.Setup(ctx, telemetry.Config{
        Endpoint: "http://localhost:4318",
    })
    if err != nil {
        slog.Error("telemetry init failed", "error", err)
        os.Exit(1)
    }
    defer func() {
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = shutdown(shutdownCtx)
    }()

    // log, tracer, and meter are ready to use.
    log.InfoContext(ctx, "service started")
    _ = tracer
    _ = meter
}
```

## Configuration

All fields are optional — sensible defaults are derived from build info when omitted.

```go
telemetry.Config{
    // service.name resource attribute.
    // Default: last segment of the module path (e.g. "myservice" from "github.com/acme/myservice").
    ServiceName: "myservice",

    // service.namespace resource attribute.
    // Default: penultimate segment of the module path (e.g. "acme").
    ServiceNamespace: "acme",

    // service.version resource attribute.
    // Default: Go module version from build info, or "unknown".
    ServiceVersion: "1.2.3",

    // Full URL of the OTLP collector. Passed to all three signal exporters.
    // Default: read from OTEL_EXPORTER_OTLP_ENDPOINT (or per-signal variants).
    Endpoint: "http://otel-collector:4318",

    // Minimum log level written to stderr.
    // Default: slog.LevelInfo.
    LogLevel: slog.LevelDebug,
}
```

### Endpoint precedence

| Source | Takes precedence |
|---|---|
| `Config.Endpoint` (in code) | Highest |
| `OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT` env var | Per-signal override |
| `OTEL_EXPORTER_OTLP_ENDPOINT` env var | Fallback |
| `localhost:4318` | SDK default |

Per-signal env vars (`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`) still work when `Config.Endpoint` is empty, so you can route signals to different collectors via the environment without changing code.

### Authentication

If your collector requires authentication, set the standard `OTEL_EXPORTER_OTLP_HEADERS` environment variable. The OTel SDK reads it automatically and attaches the headers to every export request (traces, metrics, and logs):

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT="https://otel.example.com:4318"
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>"
```

The header format is `key=value` pairs separated by commas:

```sh
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>,X-Org-Id=my-org"
```

Per-signal header vars are also supported and take precedence over the generic one:

| Variable | Applies to |
|---|---|
| `OTEL_EXPORTER_OTLP_HEADERS` | All signals (fallback) |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | Traces only |
| `OTEL_EXPORTER_OTLP_METRICS_HEADERS` | Metrics only |
| `OTEL_EXPORTER_OTLP_LOGS_HEADERS` | Logs only |

No code changes are needed — `Config.Endpoint` and auth headers are fully orthogonal.

## What Setup returns

```go
shutdown func(context.Context) error  // flushes and closes all exporters
log      *slog.Logger                 // fans out to OTel log pipeline + stderr JSON
tracer   trace.Tracer                 // scoped to ServiceName
meter    metric.Meter                 // scoped to ServiceName
err      error
```

Call `shutdown` on the way out — it flushes buffered spans, metrics, and log records. Pass a context with a timeout to cap the flush time:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
_ = shutdown(shutdownCtx)
```

## HTTP middleware

`NewMiddleware` creates an HTTP middleware that automatically instruments every route:

| Signal | Instrument | Description |
|---|---|---|
| Trace | Server span | One span per request, named `"METHOD /route/pattern"` |
| Metric | `http.server.request.count` | `Int64Counter` — total requests |
| Metric | `http.server.request.duration` | `Float64Histogram` — latency in seconds |
| Metric | `http.server.active_requests` | `Int64UpDownCounter` — in-flight requests |

All metrics carry `http.request.method`, `http.route`, and `http.response.status_code` attributes. The route label is the **pattern** (e.g. `/users/{id}`), not the concrete path — requires Go 1.22+ `http.ServeMux`.

```go
mw, err := telemetry.NewMiddleware(tracer, meter)
if err != nil {
    log.Error("middleware init failed", "error", err)
    os.Exit(1)
}

mux := http.NewServeMux()
mux.HandleFunc("/users/{id}", usersHandler)
mux.HandleFunc("/health",     healthHandler)

server := &http.Server{
    Addr:    ":8080",
    Handler: mw.Wrap(mux), // wrap the entire mux
}
```

Incoming W3C `traceparent` / `tracestate` headers are automatically extracted, so distributed traces propagate through the service boundary without any extra code.

## Tracing

### Trace convenience

`Trace` wraps the create-span / defer-end / record-error boilerplate into a single call:

```go
err := telemetry.Trace(ctx, tracer, "process-order", func(ctx context.Context, span trace.Span) error {
    span.SetAttributes(attribute.Int("order.id", orderID))

    // Nested call — automatically becomes a child span.
    return telemetry.Trace(ctx, tracer, "validate-order", func(ctx context.Context, span trace.Span) error {
        return validateOrder(ctx, orderID)
    })
})
```

If `fn` returns an error, the span automatically records the error and sets its status to `Error`. The span is always ended when `fn` returns.

### Raw tracer

For full control, use the tracer directly:

```go
ctx, span := tracer.Start(ctx, "process-order",
    trace.WithAttributes(attribute.Int("order.id", orderID)),
)
defer span.End()

if err := validateOrder(ctx, orderID); err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, "order validation failed")
    return err
}
```

### Span attributes and events

```go
ctx, span := tracer.Start(ctx, "upload-file")
defer span.End()

span.SetAttributes(
    attribute.String("file.name", filename),
    attribute.Int64("file.size_bytes", size),
)

span.AddEvent("virus-scan-passed")

if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, "upload failed")
}
```

## Metrics

### Metric wrappers

The library provides thin wrapper types with a simplified attribute API — pass `attribute.KeyValue` values directly instead of wrapping them in `metric.WithAttributes()`:

#### Counter — count occurrences

```go
counter, err := telemetry.NewCounter(meter, "orders.placed", metric.WithUnit("{order}"))
counter.Add(ctx, 1, attribute.String("method", "card"))
```

#### Histogram — measure distributions

```go
hist, err := telemetry.NewHistogram(meter, "op.duration", metric.WithUnit("ms"))
hist.Record(ctx, 42.0)
```

`Histogram.Time` measures a function's wall-clock duration and records it in the histogram's unit:

```go
hist, _ := telemetry.NewHistogram(meter, "db.query.duration", metric.WithUnit("ms"))

err := hist.Time(ctx, func(ctx context.Context) error {
    return db.QueryRowContext(ctx, "SELECT ...").Scan(&result)
})
```

If the function returns an error and the context carries an active span, the span's status is set to `Error` and the error is recorded. Supported time units: `"s"`, `"ms"`, `"us"`, `"ns"`, `"min"`, `"h"`. Defaults to seconds if the unit is empty or unrecognised.

#### Gauge — track a current value

```go
gauge, err := telemetry.NewGauge(meter, "cpu.temperature", metric.WithUnit("Cel"))
gauge.Record(ctx, 17.5)
```

#### UpDownCounter — values that go up and down

```go
udc, err := telemetry.NewUpDownCounter(meter, "db.connections")
udc.Increment(ctx)
udc.Decrement(ctx)
udc.Add(ctx, 5, attribute.String("pool", "primary"))
```

### Raw meter

For instrument types or options not covered by the wrappers, use the meter directly:

```go
orderCount, err := meter.Int64Counter("orders.processed",
    metric.WithDescription("Total number of orders processed"),
    metric.WithUnit("{order}"),
)
orderCount.Add(ctx, 1, metric.WithAttributes(
    attribute.String("order.status", status),
))
```

## Logging

`Setup` returns a `*slog.Logger` that fans out to two sinks:

- **OTel log pipeline** — records are exported to the collector via OTLP
- **stderr** — JSON-formatted records, filtered at `Config.LogLevel`

Both sinks are backed by `TraceHandler`, which automatically appends `trace_id` and `span_id` to every record when an active span is present in the context. Use `log.InfoContext(ctx, ...)` (not `log.Info(...)`) to get trace correlation.

```go
ctx, span := tracer.Start(ctx, "process-order")
defer span.End()

log.InfoContext(ctx, "order received", "order_id", 42)
// stderr output includes: {"msg":"order received","order_id":42,"trace_id":"...","span_id":"..."}
```

### Child loggers

Use `With` to create a sub-logger that carries shared fields across all calls:

```go
reqLog := log.With("request_id", requestID, "user_id", userID)
reqLog.InfoContext(ctx, "handler started")
reqLog.InfoContext(ctx, "handler complete", "status", 200)
```

### Additional logger instances

If you need a second logger (e.g. for a background worker), call `NewLogger` after `Setup`:

```go
workerLog := telemetry.NewLogger("worker", slog.LevelWarn)
```

## Testing

The `oteltest` sub-package provides in-memory trace and metric providers for testing. No collector needed — all telemetry is captured in memory and cleaned up automatically via `t.Cleanup`.

```go
import (
    "context"
    "testing"

    telemetry "github.com/bitsmithy/go-otel"
    "github.com/bitsmithy/go-otel/oteltest"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"
)

func TestProcessOrder(t *testing.T) {
    h := oteltest.Setup(t)
    ctx := context.Background()

    err := telemetry.Trace(ctx, h.Tracer, "process-order", func(ctx context.Context, span trace.Span) error {
        return processOrder(ctx, orderID)
    })

    // Assert spans
    spans := h.Spans()
    if len(spans) != 1 {
        t.Fatalf("expected 1 span, got %d", len(spans))
    }
    if spans[0].Status.Code != codes.Ok {
        t.Errorf("span status = %v, want Ok", spans[0].Status.Code)
    }

    // Assert metrics
    rm := h.Metrics(t)
    m := oteltest.FindMetric(rm, "orders.placed")
    if m == nil {
        t.Fatal("metric not found")
    }
}
```

### Harness API

| Method | Returns | Description |
|---|---|---|
| `oteltest.Setup(t)` | `*Harness` | In-memory tracer + meter, auto-cleaned up |
| `h.Tracer` | `trace.Tracer` | In-memory tracer for creating spans |
| `h.Meter` | `metric.Meter` | In-memory meter for creating instruments |
| `h.Spans()` | `[]tracetest.SpanStub` | All completed spans |
| `h.Metrics(t)` | `metricdata.ResourceMetrics` | All recorded metrics |
| `oteltest.FindMetric(rm, name)` | `*metricdata.Metrics` | Find a metric by name |
| `oteltest.MetricAttrs(attrs)` | `map[string]string` | Extract attributes as a map |

## Advanced

### Telemetry after context cancellation

When a request context is cancelled (e.g. a timeout fires), the OTel SDK silently drops any telemetry emitted on that context. Use `DetachedContext` to emit final logs and metrics after a potentially-cancelled operation:

```go
start := time.Now()
result, err := doWork(ctx)
dur := time.Since(start).Seconds()

// ctx may be cancelled here — detach before recording telemetry.
tctx := telemetry.DetachedContext(ctx)
if err != nil {
    log.ErrorContext(tctx, "work failed", "duration_s", dur, "error", err)
    return
}
log.InfoContext(tctx, "work done", "duration_s", dur)
```

`DetachedContext` returns a fresh, never-cancelled context that still carries the active span, so trace correlation is preserved.

### slog handlers

Both handlers are exported for use in custom logging setups.

**TraceHandler** wraps any `slog.Handler` and injects `trace_id` and `span_id` into records when an active span is present:

```go
base := slog.NewJSONHandler(os.Stderr, nil)
log := slog.New(&telemetry.TraceHandler{Handler: base})
```

**FanoutHandler** sends each record to multiple handlers:

```go
log := slog.New(telemetry.FanoutHandler{
    slog.NewJSONHandler(os.Stderr, nil),
    slog.NewTextHandler(logFile, nil),
})
```

## Development

### Prerequisites

- [Go 1.24+](https://go.dev/dl/)
- [lefthook](https://github.com/evilmartians/lefthook) — git hooks manager
- [gofumpt](https://github.com/mvdan/gofumpt), [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports), [gci](https://github.com/daixiang0/gci) — formatting
- [golangci-lint](https://golangci-lint.run/) — linting
- [gotestsum](https://github.com/gotestyourself/gotestsum) — test runner

### Setup

```sh
git clone https://github.com/bitsmithy/go-otel.git
cd go-otel
lefthook install
```

### Pre-commit hooks

Lefthook runs a two-stage pipeline on every commit:

1. **Fixers** (sequential): gofumpt → goimports → gci → golangci-lint --fix
2. **Checks** (parallel): go vet, gotestsum

Fixers auto-format staged Go files and re-stage them. If a fixer fails, checks are skipped.

### CI

Pull requests run two GitHub Actions workflows:

| Workflow | Jobs |
|---|---|
| **Check** | gofumpt diff, go vet, golangci-lint, govulncheck |
| **Test** | gotestsum |

## License

MIT — see [LICENSE](LICENSE).
