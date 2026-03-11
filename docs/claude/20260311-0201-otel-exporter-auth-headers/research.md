# Research: OTLP Exporter Auth Headers

## Summary

The `go-otel` module needs to support passing authorization headers to OTLP HTTP exporters. The user has added auth to their OTel collector endpoint and needs headers (via `OTEL_EXPORTER_OTLP_HEADERS`) forwarded when exporting traces, metrics, and logs.

## Current State

### Module Structure

- `otel.go` — `Config` struct + `Setup()` function. Creates three OTLP HTTP exporters (traces, metrics, logs) and wires them into global providers.
- `handler.go` — `TraceHandler` (injects trace/span IDs into slog) + `FanoutHandler` (multi-sink slog handler).
- `middleware.go` — HTTP middleware for automatic span/metric instrumentation.
- Test files: `otel_test.go`, `handler_test.go`, `middleware_test.go`.

### Config Struct (`otel.go:31-54`)

```go
type Config struct {
    ServiceName      string
    ServiceNamespace string
    ServiceVersion   string
    Endpoint         string    // Passed via WithEndpointURL to all exporters
    LogLevel         slog.Level
}
```

No `Headers` field exists. The `Endpoint` field is the only exporter config override — when set, it's passed via `WithEndpointURL()` to all three exporters.

### Exporter Creation Pattern (`otel.go:113-160`)

Each exporter follows the same pattern:

```go
var traceOpts []otlptracehttp.Option
if cfg.Endpoint != "" {
    traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
}
traceExp, err := otlptracehttp.New(ctx, traceOpts...)
```

Repeated for `otlpmetrichttp` and `otlploghttp`.

## SDK Behavior: Environment Variables

The OTel Go SDK's OTLP HTTP exporters **already read `OTEL_EXPORTER_OTLP_HEADERS` automatically** from the environment. From the SDK docs:

> `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS` (default: none) — key-value pairs used as headers associated with HTTP requests. Format: `key1=value1,key2=value2`. Signal-specific vars take precedence.

**Programmatic `WithHeaders()` overrides the env var.** If no `WithHeaders()` is called, the env var is used. This means:

1. **If the user just sets `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer token123`**, it already works with the current code — no changes needed.
2. **But** the `Config` struct doesn't expose a `Headers` field for programmatic override, which breaks the pattern established by `Endpoint`.

## Design Decision

Two approaches:

### Option A: Add `Headers map[string]string` to Config

Mirrors the `Endpoint` pattern. When set, pass `WithHeaders()` to all three exporters. When empty, the SDK falls back to env vars automatically.

**Pros**: Consistent API, programmatic control, testable.
**Cons**: Slightly more code.

### Option B: Do nothing, document the env var

The SDK already handles `OTEL_EXPORTER_OTLP_HEADERS`.

**Pros**: Zero code change.
**Cons**: Inconsistent — `Endpoint` has a programmatic override but headers don't. Users who configure via code (not env vars) have no way to set headers.

## Recommendation

**Option A** — add `Headers map[string]string` to `Config`. This follows the existing pattern (`Endpoint` has a struct field + env var fallback) and gives callers programmatic control. The implementation is straightforward: append `WithHeaders(cfg.Headers)` to each exporter's option slice when `len(cfg.Headers) > 0`.

## Key Files to Modify

| File | Change |
|---|---|
| `otel.go` | Add `Headers` field to `Config`, pass `WithHeaders()` to all three exporters |
| `otel_test.go` | Add test for `Headers` config propagation |

## SDK API Reference

All three exporter packages expose `WithHeaders(map[string]string)`:

- `otlptracehttp.WithHeaders()`
- `otlpmetrichttp.WithHeaders()`
- `otlploghttp.WithHeaders()`

Each accepts `map[string]string` and sets them as additional HTTP headers on every export request.
