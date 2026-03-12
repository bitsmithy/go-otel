# Research: Feature Parity Between otel-rb and go-otel

## Overview

This document compares the **otel-rb** Ruby library and the **go-otel** Go library — both thin, opinionated wrappers around OpenTelemetry that provide single-call setup for all three signals (traces, metrics, logs) over OTLP/HTTP. The goal is to identify the feature gap and determine what can be ported to achieve ergonomic parity, accounting for Go's language constraints.

## Architecture Comparison

### Shared Design Philosophy

Both libraries share the same core idea:

1. **Single-call setup** — one function wires traces, metrics, and logs
2. **OTLP/HTTP only** — no stdout/Zipkin/Jaeger in the setup path
3. **All three signals always** — traces, metrics, logs are hard dependencies
4. **Sensible defaults** — service name/namespace/version derived automatically
5. **HTTP middleware** — automatic span + 3 standard metrics per request
6. **Standard env vars** — respects `OTEL_EXPORTER_OTLP_*` for endpoint/headers

### Current go-otel Surface Area

| Export | Purpose |
|--------|---------|
| `Config` struct | 5 fields: ServiceName, ServiceNamespace, ServiceVersion, Endpoint, LogLevel |
| `Setup()` | Returns (shutdown, logger, tracer, meter, error) |
| `NewLogger()` | Creates additional logger instances |
| `NewMiddleware()` | Creates HTTP middleware with 3 metrics |
| `DetachedContext()` | Fresh context preserving active span |
| `TraceHandler` | slog handler injecting trace_id/span_id |
| `FanoutHandler` | slog handler fanning out to multiple sinks |

### otel-rb Surface Area (additional to what go-otel has)

| Feature | Purpose |
|---------|---------|
| `Telemetry.trace(name, attrs:) { \|span\| ... }` | Convenience tracing with auto-finish |
| `Telemetry.counter/histogram/gauge/up_down_counter` | Metric convenience methods with handle + fire-and-forget forms |
| `Telemetry.time(name) { block }` | Shorthand histogram timing |
| `Instruments::Counter/Histogram/Gauge/UpDownCounter` | Ergonomic metric wrappers with caching |
| `Telemetry.log(level, message)` | Logging convenience |
| `Telemetry.logger` | Logger accessor |
| `Telemetry.test_mode!` / `Telemetry.reset!` | Test helpers |
| `NotSetupError` | Guard against use-before-setup |
| `LogBridge` | Intercepts Rails.logger → OTel emission |
| `TraceFormatter` | Decorates log output with trace/span IDs |

## Feature Gap Analysis

### Gap 1: Tracing Convenience — `Trace()` Function

**otel-rb:**
```ruby
Telemetry.trace("orders.process", attrs: { "order.id" => id }) do |span|
  span.set_attribute("order.items", count)
  Telemetry.trace("orders.charge") { |child| charge(order) }
end
```

**go-otel (current):**
```go
ctx, span := tracer.Start(ctx, "orders.process",
    trace.WithAttributes(attribute.Int("order.id", id)))
defer span.End()
// manually create child span...
```

**Gap:** Go requires 3 lines (Start + defer End + error handling) for every span. otel-rb wraps this in a single call with automatic span finish.

**Go-idiomatic solution:** A `Trace(ctx, tracer, name, attrs, func)` helper that creates a span, defers End, and calls the function with the span. Go's lack of implicit context means we must pass `ctx` and `tracer` explicitly — but we can still eliminate boilerplate.

**Achievable:** Yes. The function signature would be:
```go
func Trace(ctx context.Context, tracer trace.Tracer, name string, fn func(ctx context.Context, span trace.Span) error, opts ...trace.SpanStartOption) error
```

### Gap 2: Metric Convenience Wrappers

**otel-rb has three usage patterns:**

1. **Handle form** — returns a reusable, cached instrument:
   ```ruby
   orders = Telemetry.counter("orders.placed", unit: "{order}")
   orders.add(1, "method" => "card")
   ```

2. **Fire-and-forget** — records immediately:
   ```ruby
   Telemetry.counter("orders.placed", 1, "method" => "card")
   ```

3. **Block timing** (histogram only):
   ```ruby
   Telemetry.time("orders.duration") { charge(order) }
   ```

**go-otel (current):** Returns raw `metric.Meter` from Setup. Users must:
- Call `meter.Int64Counter(...)` etc. themselves
- Handle the error from instrument creation
- Store the instrument somewhere for reuse
- Pass `metric.WithAttributes(attribute.String(...))` for every recording

**Go-idiomatic solution:** Wrapper types that simplify the raw OTel metric API:

- `Counter` — wraps `metric.Int64Counter`, provides `Add(ctx, value, attrs...)` with simpler attribute passing
- `Histogram` — wraps `metric.Float64Histogram`, adds `Time(ctx, fn, attrs...)` for block timing
- `Gauge` — wraps `metric.Float64Gauge`, provides `Record(ctx, value, attrs...)`
- `UpDownCounter` — wraps `metric.Int64UpDownCounter`, adds `Increment/Decrement` semantic methods
- Constructor functions like `NewCounter(meter, name, opts...)` that handle error + creation

**Fire-and-forget with caching:** In Ruby, `Telemetry.counter("name", 1)` works because instruments are cached by name. In Go, we can provide a `Metrics` struct that caches instruments and offers `Counter(name, value, attrs)` / `Histogram(name, value, attrs)` etc. This is a bigger departure from Go conventions (error handling deferred, implicit caching). A simpler approach: just provide the wrapper types and let users create+store them explicitly (Go way), but make creation and recording ergonomic.

**Achievable:** Partially. The wrapper types and `Time()` are natural fits. Fire-and-forget with implicit caching is un-Go-like and should be skipped in favor of explicit instrument creation.

### Gap 3: Histogram Timing

**otel-rb:**
```ruby
Telemetry.time("orders.charge") { charge(order) }
# or
hist = Telemetry.histogram("orders.duration", unit: "ms")
hist.time { charge(order) }
hist.time("queue" => "default") { charge(order) }
```

The histogram wrapper converts elapsed time to the histogram's declared unit (ms, s, us, etc.) automatically.

**go-otel:** No timing helper exists.

**Go-idiomatic solution:** A `Time(ctx, fn, attrs...)` method on the Histogram wrapper that uses `time.Since()` and converts to the histogram's unit. Also a standalone `Time()` function.

**Achievable:** Yes.

### Gap 4: UpDownCounter Semantic Methods

**otel-rb:**
```ruby
connections = Telemetry.up_down_counter("db.connections")
connections.increment        # +1
connections.increment(5)     # +5
connections.decrement        # -1
connections.decrement(3)     # -3
```

**go-otel:** Raw `metric.Int64UpDownCounter` only has `Add(ctx, value, opts...)`.

**Go-idiomatic solution:** Wrapper with `Increment(ctx, n, attrs...)` and `Decrement(ctx, n, attrs...)`.

**Achievable:** Yes.

### Gap 5: Simplified Attribute Passing

**otel-rb:**
```ruby
counter.add(1, "payment.method" => "card", "region" => "us")
```

**go-otel (current):**
```go
counter.Add(ctx, 1, metric.WithAttributes(
    attribute.String("payment.method", "card"),
    attribute.String("region", "us"),
))
```

**Gap:** OTel Go's attribute API is verbose. Every attribute needs `attribute.String/Int/Bool/Float64()` and must be wrapped in `metric.WithAttributes()`.

**Go-idiomatic solution:** An `Attrs()` helper function that accepts variadic `attribute.KeyValue` but provides a shorter import path. We can't match Ruby's hash syntax, but we can provide convenience. The wrapper types could accept `...attribute.KeyValue` directly instead of `...metric.MeasurementOption`.

**Achievable:** Partially. We can simplify by having wrapper methods accept `...attribute.KeyValue` directly.

### Gap 6: Test Helpers

**otel-rb:**
```ruby
# test/test_helper.rb
require 'telemetry/test'  # activates test mode

# Each test:
Telemetry.setup  # in-memory exporters, no network
# ... test ...
Telemetry.reset!  # clear state
```

**go-otel:** No test helpers. Tests in the library itself use raw SDK in-memory exporters and manual setup.

**Go-idiomatic solution:** A `testing` sub-package (e.g., `otel/oteltest`) that provides:
- `SetupTest(t) → (tracer, meter, logger, spans, metrics)` — in-memory exporters, auto-cleanup via `t.Cleanup`
- Returns accessors for recorded spans and metrics for assertions

**Achievable:** Yes. This is a natural fit for Go's testing conventions.

### Gap 7: LogBridge / TraceFormatter (Rails-specific)

**otel-rb:** `LogBridge` intercepts calls to `Rails.logger` and emits OTel log records. `TraceFormatter` decorates log output with trace/span IDs.

**go-otel:** Already has `TraceHandler` (injects trace_id/span_id into slog records) and `FanoutHandler` (dual-sink to OTel + stderr). These achieve the same outcome as LogBridge + TraceFormatter through Go's slog handler composition.

**Gap:** None — this is already at parity through different (idiomatic) mechanisms.

### Gap 8: NotSetupError Guard

**otel-rb:** Raises `NotSetupError` if API used before `Telemetry.setup`.

**go-otel:** Not applicable. Go returns instances from `Setup()` — you can't call methods on a nil tracer/meter without a nil pointer panic, which is the Go equivalent of this guard. The explicit return-values design prevents use-before-setup by construction.

**Gap:** None — Go's design makes this unnecessary.

## Key Files

### otel-rb

| File | Lines | Role |
|------|-------|------|
| `lib/telemetry.rb` | 231 | Main module: setup, trace, metric dispatch, log delegation |
| `lib/telemetry/setup.rb` | 105 | OTel provider/exporter initialization |
| `lib/telemetry/instruments.rb` | 111 | Counter, Histogram, Gauge, UpDownCounter wrappers |
| `lib/telemetry/metering.rb` | 64 | Metric dispatch and caching (parse_rest, dispatch, fetch_instrument) |
| `lib/telemetry/middleware.rb` | 103 | Rack middleware: spans + 3 HTTP metrics |
| `lib/telemetry/logger.rb` | 93 | OTel logger with Rails.logger delegation |
| `lib/telemetry/log_bridge.rb` | 70 | Rails.logger interceptor for OTel emission |
| `lib/telemetry/trace_formatter.rb` | 35 | Logger formatter with trace/span ID decoration |
| `lib/telemetry/config.rb` | 37 | Config struct with defaults |
| `lib/telemetry/test.rb` | 7 | Test mode activation |

### go-otel

| File | Lines | Role |
|------|-------|------|
| `otel.go` | 220 | Setup, NewLogger, DetachedContext, Config |
| `middleware.go` | 129 | HTTP middleware: spans + 3 HTTP metrics |
| `handler.go` | 76 | TraceHandler + FanoutHandler for slog |
| `otel_test.go` | 153 | Setup tests |
| `middleware_test.go` | 408 | Middleware tests |
| `handler_test.go` | 175 | Handler tests |

## What to Build (Priority Order)

### P0 — Core Ergonomic Gaps

1. **Metric wrapper types** (`metric.go` + `metric_test.go`)
   - `Counter` — `Add(ctx, value, attrs...)`
   - `Histogram` — `Record(ctx, value, attrs...)`, `Time(ctx, fn, attrs...)`
   - `Gauge` — `Record(ctx, value, attrs...)`
   - `UpDownCounter` — `Increment(ctx, n, attrs...)`, `Decrement(ctx, n, attrs...)`
   - Constructor functions: `NewCounter(meter, name, opts...)` etc.
   - Unit-aware time conversion for Histogram.Time

2. **Trace convenience** (`trace.go` + `trace_test.go`)
   - `Trace(ctx, tracer, name, fn, opts...)` — creates span, defers End, calls fn

### P1 — Developer Experience

3. **Test helpers** (`oteltest/` sub-package)
   - `Setup(t)` — returns in-memory tracer/meter/logger + span/metric accessors
   - Auto-cleanup via `t.Cleanup`

### P2 — Nice to Have

4. **`Time()` standalone** — top-level `Time(ctx, hist, fn, attrs...)` for one-off timing without a Histogram wrapper

## Patterns & Conventions

### otel-rb Conventions Worth Preserving
- Instrument names use dots: `"orders.placed"`, `"http.server.request.count"`
- Metric units follow OTel conventions: `"{request}"`, `"ms"`, `"s"`, `"By"`
- Middleware metric names and attributes match between both libraries
- Three standard HTTP metrics: request count, request duration, active requests

### Go-Specific Conventions to Follow
- Constructors return `(T, error)` — never panic
- Methods accept `context.Context` as first parameter
- Attributes use `attribute.KeyValue` — no map[string]interface{}
- Metric options use `metric.WithAttributes(...)` pattern from OTel SDK
- Test helpers use `*testing.T` and `t.Cleanup` for teardown

## Edge Cases & Gotchas

1. **Histogram unit conversion**: otel-rb converts elapsed seconds to the histogram's declared unit (ms → ×1000, us → ×1000000, etc.). Go implementation must do the same, using `time.Duration` naturally.

2. **Metric type mismatch**: otel-rb uses `create_counter` (which returns a counter that accepts floats in Ruby). Go has separate `Int64Counter` and `Float64Counter`. We need to decide: int64 for counters/up-down-counters (natural for counts), float64 for histograms/gauges (natural for measurements).

3. **Instrument caching**: otel-rb caches instruments by `[type, name]`. In Go, the OTel SDK already handles instrument identity — creating the same instrument twice returns the same underlying object. We can skip explicit caching and let users manage their own instances (more Go-idiomatic).

4. **Error handling in wrappers**: Ruby wrappers never error (instruments are created lazily, errors silently ignored). Go wrappers should return errors from constructors but not from recording methods (matching OTel Go SDK convention where Add/Record don't return errors).

5. **`Trace()` error propagation**: otel-rb's `trace` block returns the block's value. Go's `Trace()` should return error (Go convention) and set span status on error. The callback signature `func(ctx, span) error` is more useful than returning an arbitrary value.

## Current State

go-otel is a solid foundation with Setup, middleware, and logging fully implemented. The main gap is in developer ergonomics for day-to-day tracing and metric recording. The library does the hard work (setup, shutdown, propagation) but leaves the repetitive work (span lifecycle, instrument creation, attribute passing) to the user.

otel-rb has invested heavily in making the "write telemetry" side ergonomic, not just the "configure telemetry" side. Closing this gap in go-otel would make it significantly more pleasant to use.
