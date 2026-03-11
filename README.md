# go-otel

A thin, opinionated OpenTelemetry setup library for Go services. One call wires up traces, metrics, and logs over OTLP/HTTP, returns a pre-configured `*slog.Logger`, and exposes HTTP middleware for `http.ServeMux`.

## Requirements

- Go 1.22 or later
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

### Additional logger instances

If you need a second logger (e.g. for a background worker), call `NewLogger` after `Setup`:

```go
workerLog := telemetry.NewLogger("worker", slog.LevelWarn)
```

## Using the tracer

Use the tracer to record units of work as spans. Child spans are created by passing the context returned by the parent `Start` call.

```go
func processOrder(ctx context.Context, tracer trace.Tracer, orderID int) error {
    ctx, span := tracer.Start(ctx, "process-order",
        trace.WithAttributes(attribute.Int("order.id", orderID)),
    )
    defer span.End()

    if err := validateOrder(ctx, tracer, orderID); err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, "order validation failed")
        return err
    }

    span.SetStatus(codes.Ok, "")
    return nil
}

func validateOrder(ctx context.Context, tracer trace.Tracer, orderID int) error {
    _, span := tracer.Start(ctx, "validate-order")
    defer span.End()

    // ... validation logic ...
    return nil
}
```

Spans are automatically linked — `validate-order` will appear as a child of `process-order` in your tracing backend.

### Span attributes and events

```go
ctx, span := tracer.Start(ctx, "upload-file")
defer span.End()

// Attach structured data to the span.
span.SetAttributes(
    attribute.String("file.name", filename),
    attribute.Int64("file.size_bytes", size),
)

// Record a point-in-time event within the span.
span.AddEvent("virus-scan-passed")

// Mark the span as failed.
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, "upload failed")
}
```

Required imports:

```go
import (
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"
)
```

## Using the meter

Use the meter to create and record metric instruments. Create instruments once (e.g. in a constructor) and reuse them across calls.

### Counter — count occurrences

```go
type OrderProcessor struct {
    orderCount metric.Int64Counter
}

func NewOrderProcessor(meter metric.Meter) (*OrderProcessor, error) {
    orderCount, err := meter.Int64Counter("orders.processed",
        metric.WithDescription("Total number of orders processed"),
        metric.WithUnit("{order}"),
    )
    if err != nil {
        return nil, err
    }
    return &OrderProcessor{orderCount: orderCount}, nil
}

func (p *OrderProcessor) Process(ctx context.Context, status string) {
    p.orderCount.Add(ctx, 1, metric.WithAttributes(
        attribute.String("order.status", status),
    ))
}
```

### Histogram — measure distributions

```go
duration, err := meter.Float64Histogram("order.processing.duration",
    metric.WithDescription("Time taken to process an order"),
    metric.WithUnit("s"),
)

start := time.Now()
err = processOrder(ctx, orderID)
duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
    attribute.Bool("order.success", err == nil),
))
```

### Gauge — track a current value

```go
queueDepth, err := meter.Int64UpDownCounter("orders.queue.depth",
    metric.WithDescription("Number of orders waiting to be processed"),
    metric.WithUnit("{order}"),
)

queueDepth.Add(ctx, 1)  // order enqueued
queueDepth.Add(ctx, -1) // order dequeued
```

Required imports:

```go
import (
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/metric"
)
```

## Using the logger

Always use the `Context` variants (`InfoContext`, `ErrorContext`, etc.) so that `trace_id` and `span_id` are automatically injected when a span is active.

```go
// Structured key-value pairs.
log.InfoContext(ctx, "order received", "order_id", orderID, "customer", email)

// Warn with context.
log.WarnContext(ctx, "payment retry", "attempt", 2, "order_id", orderID)

// Error — include the error value as "error".
if err != nil {
    log.ErrorContext(ctx, "order failed", "order_id", orderID, "error", err)
    return err
}
```

Example stderr output when a span is active:

```json
{"time":"2026-03-06T14:00:00Z","level":"INFO","msg":"order received","order_id":99,"customer":"alice@example.com","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
```

### Child loggers

Use `With` to create a sub-logger that carries shared fields across all calls — useful for request-scoped or component-scoped logging:

```go
reqLog := log.With("request_id", requestID, "user_id", userID)
reqLog.InfoContext(ctx, "handler started")
reqLog.InfoContext(ctx, "handler complete", "status", 200)
```

## Telemetry after context cancellation

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

## slog handlers

Both handlers are exported for use in custom logging setups.

### TraceHandler

Wraps any `slog.Handler` and injects `trace_id` and `span_id` into records when an active span is present:

```go
base := slog.NewJSONHandler(os.Stderr, nil)
log := slog.New(&telemetry.TraceHandler{Handler: base})
```

### FanoutHandler

Sends each record to multiple handlers. Useful for routing logs to more than one sink:

```go
log := slog.New(telemetry.FanoutHandler{
    slog.NewJSONHandler(os.Stderr, nil),
    slog.NewTextHandler(logFile, nil),
})
```

## License

MIT — see [LICENSE](LICENSE).
