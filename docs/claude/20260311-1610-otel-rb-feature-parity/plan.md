# Plan: Feature Parity with otel-rb

## Goal

Add metric wrapper types, a trace convenience function, and a test helpers sub-package to go-otel — closing the ergonomic gap with otel-rb while staying idiomatic to Go.

## Research Reference

`docs/claude/20260311-1610-otel-rb-feature-parity/research.md`

## Approach

Three new files in the root package (`metric.go`, `trace.go`) plus one new sub-package (`oteltest/`). No changes to existing files — pure additions.

**Design principles:**
- Constructors return `(T, error)` — never panic
- Recording methods accept `...attribute.KeyValue` directly (not `...metric.MeasurementOption`)
- `context.Context` is always the first parameter on recording methods
- Wrappers are thin — one struct field holding the SDK instrument, forwarding calls with attribute conversion
- Test helpers use `*testing.T` and `t.Cleanup` for teardown

## Detailed Changes

### New File: `metric.go`

Four wrapper types and their constructors. Each wraps the corresponding OTel SDK instrument and simplifies the attribute-passing API.

```go
package otel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Counter wraps metric.Int64Counter with a simplified attribute API.
type Counter struct {
	inner metric.Int64Counter
}

// NewCounter creates a Counter from the given meter.
func NewCounter(meter metric.Meter, name string, opts ...metric.Int64CounterOption) (Counter, error) {
	c, err := meter.Int64Counter(name, opts...)
	if err != nil {
		return Counter{}, fmt.Errorf("counter %s: %w", name, err)
	}
	return Counter{inner: c}, nil
}

// Add increments the counter by value with optional attributes.
func (c Counter) Add(ctx context.Context, value int64, attrs ...attribute.KeyValue) {
	c.inner.Add(ctx, value, metric.WithAttributes(attrs...))
}
```

```go
// Histogram wraps metric.Float64Histogram with a simplified attribute API
// and a Time method for measuring function duration.
type Histogram struct {
	inner metric.Float64Histogram
	unit  string
}

// NewHistogram creates a Histogram from the given meter. The unit (if set via
// metric.WithUnit) is used by Time to convert elapsed duration automatically.
// Supported time units: "s", "ms", "us", "ns", "min", "h".
func NewHistogram(meter metric.Meter, name string, opts ...metric.Float64HistogramOption) (Histogram, error) {
	h, err := meter.Float64Histogram(name, opts...)
	if err != nil {
		return Histogram{}, fmt.Errorf("histogram %s: %w", name, err)
	}
	// Extract unit from options to enable Time() conversion.
	// We parse the opts ourselves since the SDK doesn't expose the configured unit.
	unit := extractHistogramUnit(opts)
	return Histogram{inner: h, unit: unit}, nil
}

// Record records a single observation with optional attributes.
func (h Histogram) Record(ctx context.Context, value float64, attrs ...attribute.KeyValue) {
	h.inner.Record(ctx, value, metric.WithAttributes(attrs...))
}

// Time calls fn, measures its wall-clock duration, records it in the
// histogram's unit, and returns fn's error. If fn returns an error and ctx
// carries an active span, the span's status is set to Error.
func (h Histogram) Time(ctx context.Context, fn func(ctx context.Context) error, attrs ...attribute.KeyValue) error {
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	h.Record(ctx, durationIn(elapsed, h.unit), attrs...)
	if span := trace.SpanFromContext(ctx); err != nil && span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
```

Unit conversion helper (unexported):

```go
// durationIn converts a time.Duration to a float64 in the given unit.
// Defaults to seconds if the unit is empty or unrecognised.
func durationIn(d time.Duration, unit string) float64 {
	switch unit {
	case "ns":
		return float64(d.Nanoseconds())
	case "us":
		return float64(d.Microseconds())
	case "ms":
		return float64(d.Milliseconds())
	case "s", "":
		return d.Seconds()
	case "min":
		return d.Minutes()
	case "h":
		return d.Hours()
	default:
		return d.Seconds()
	}
}

// extractHistogramUnit reads the unit from histogram options.
func extractHistogramUnit(opts []metric.Float64HistogramOption) string {
	cfg := metric.NewFloat64HistogramConfig(opts...)
	return cfg.Unit()
}
```

```go
// Gauge wraps metric.Float64Gauge with a simplified attribute API.
type Gauge struct {
	inner metric.Float64Gauge
}

// NewGauge creates a Gauge from the given meter.
func NewGauge(meter metric.Meter, name string, opts ...metric.Float64GaugeOption) (Gauge, error) {
	g, err := meter.Float64Gauge(name, opts...)
	if err != nil {
		return Gauge{}, fmt.Errorf("gauge %s: %w", name, err)
	}
	return Gauge{inner: g}, nil
}

// Record records the current value with optional attributes.
func (g Gauge) Record(ctx context.Context, value float64, attrs ...attribute.KeyValue) {
	g.inner.Record(ctx, value, metric.WithAttributes(attrs...))
}
```

```go
// UpDownCounter wraps metric.Int64UpDownCounter with semantic
// Increment and Decrement methods.
type UpDownCounter struct {
	inner metric.Int64UpDownCounter
}

// NewUpDownCounter creates an UpDownCounter from the given meter.
func NewUpDownCounter(meter metric.Meter, name string, opts ...metric.Int64UpDownCounterOption) (UpDownCounter, error) {
	u, err := meter.Int64UpDownCounter(name, opts...)
	if err != nil {
		return UpDownCounter{}, fmt.Errorf("up_down_counter %s: %w", name, err)
	}
	return UpDownCounter{inner: u}, nil
}

// Add adds a delta (positive or negative) with optional attributes.
func (u UpDownCounter) Add(ctx context.Context, delta int64, attrs ...attribute.KeyValue) {
	u.inner.Add(ctx, delta, metric.WithAttributes(attrs...))
}

// Increment adds 1 with optional attributes.
func (u UpDownCounter) Increment(ctx context.Context, attrs ...attribute.KeyValue) {
	u.Add(ctx, 1, attrs...)
}

// Decrement subtracts 1 with optional attributes.
func (u UpDownCounter) Decrement(ctx context.Context, attrs ...attribute.KeyValue) {
	u.Add(ctx, -1, attrs...)
}
```

### New File: `trace.go`

A single convenience function that wraps the span lifecycle.

```go
package otel

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Trace creates a span, calls fn with the span's context and the span itself,
// and ends the span when fn returns. If fn returns an error, the span records
// the error and sets its status to Error.
//
// Usage:
//
//	err := otel.Trace(ctx, tracer, "orders.process", func(ctx context.Context, span trace.Span) error {
//	    span.SetAttributes(attribute.Int("order.id", orderID))
//	    return processOrder(ctx, orderID)
//	})
func Trace(ctx context.Context, tracer trace.Tracer, name string, fn func(ctx context.Context, span trace.Span) error, opts ...trace.SpanStartOption) error {
	ctx, span := tracer.Start(ctx, name, opts...)
	defer span.End()

	err := fn(ctx, span)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
```

### New Sub-Package: `oteltest/`

A test helper package that eliminates the boilerplate of setting up in-memory exporters, providers, and cleanup. Lives at `oteltest/oteltest.go`.

```go
package oteltest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// Harness holds in-memory tracer and meter providers for testing.
// Create one with Setup. All providers are shut down automatically
// via t.Cleanup.
type Harness struct {
	Tracer trace.Tracer
	Meter  metric.Meter
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// Setup creates in-memory trace and metric providers scoped to t.
// Providers are shut down via t.Cleanup — no manual teardown needed.
func Setup(t *testing.T) *Harness {
	t.Helper()

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	return &Harness{
		Tracer: tp.Tracer("test"),
		Meter:  mp.Meter("test"),
		spans:  sr,
		reader: reader,
	}
}

// Spans returns all completed spans.
func (h *Harness) Spans() []tracetest.SpanStub {
	ended := h.spans.Ended()
	stubs := make([]tracetest.SpanStub, len(ended))
	for i, s := range ended {
		stubs[i] = tracetest.SpanStubFromReadOnlySpan(s)
	}
	return stubs
}

// Metrics collects and returns all recorded metrics.
func (h *Harness) Metrics(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return rm
}

// FindMetric searches collected metrics for one with the given name.
// Returns nil if not found.
func FindMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// MetricAttrs extracts attribute key-value pairs from a metric data point's
// attribute set as a map for easy assertion.
func MetricAttrs(attrs attribute.Set) map[string]string {
	m := make(map[string]string)
	for _, kv := range attrs.ToSlice() {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}
```

### New File: `oteltest/oteltest_test.go`

Self-tests for the oteltest package (see Testing Strategy below).

## Dependencies

No new external dependencies. All required packages (`go.opentelemetry.io/otel/sdk/trace/tracetest`, `go.opentelemetry.io/otel/sdk/metric`, etc.) are already in `go.mod`.

The `oteltest/` sub-package lives in the same module — no separate `go.mod` needed.

## Considerations & Trade-offs

### Why `...attribute.KeyValue` instead of `...metric.MeasurementOption`

The OTel SDK's `metric.WithAttributes(attribute.String("k", "v"))` is verbose. Our wrappers accept `...attribute.KeyValue` directly and wrap them internally. This means users write:

```go
counter.Add(ctx, 1, attribute.String("method", "card"))
```

Instead of:

```go
counter.Add(ctx, 1, metric.WithAttributes(attribute.String("method", "card")))
```

The trade-off: users lose access to other `MeasurementOption` types (there are none today — `WithAttributes` is the only option). If the SDK adds new options in the future, we can add an `AddWithOptions` method.

### Why `Histogram.Time()` sets span error status

otel-rb's `time` method is pure metric recording. Our Go version also records the error on the active span if one exists. This is more useful — timing an operation and recording its failure are almost always done together. If a user wants pure timing without span interaction, they can use `Record()` directly with their own timing logic.

### Why not fire-and-forget / caching

otel-rb supports `Telemetry.counter("name", 1)` — create-or-lookup + record in one call. This requires module-level state and implicit caching, which is un-Go-like. Go users create instruments at startup and pass them where needed. The OTel SDK already deduplicates instruments by name, so explicit caching adds no value.

### Why `Harness` instead of returning 5 values

`oteltest.Setup(t)` returns a `*Harness` rather than `(tracer, meter, spans, reader)` because:
1. Adding fields later doesn't break callers
2. Methods like `Spans()` and `Metrics(t)` encapsulate conversion/collection boilerplate
3. Consistent with how the existing test helpers work, but with less repetition

### `extractHistogramUnit` approach

The OTel Go SDK's `metric.NewFloat64HistogramConfig(opts...)` is a public API that applies options to a config struct. We use it to extract the unit string from the user's options, then store it for `Time()` conversion. This avoids requiring users to pass the unit twice.

## Migration / Data Changes

None. Pure additions — no existing API changes.

## Testing Strategy

### `metric_test.go`

All metric wrapper tests use in-memory providers. Tests are structured as: create instrument → record value → collect metrics → assert.

1. **TestNewCounter_ReturnsWorkingCounter** — create counter via NewCounter, call Add with value 5 and attributes, collect metrics, assert Sum[int64] data point has value 5 and correct attributes.

2. **TestCounter_MultipleAdds** — call Add three times with value 1, assert total is 3.

3. **TestNewHistogram_ReturnsWorkingHistogram** — create histogram via NewHistogram, call Record with value 42.0, collect metrics, assert Histogram[float64] has count 1.

4. **TestHistogram_Time_RecordsDuration** — create histogram with unit "ms", call Time with a function that sleeps 10ms, collect metrics, assert histogram count is 1 and sum is >= 10.

5. **TestHistogram_Time_ReturnsError** — call Time with a function that returns an error, assert Time returns the same error.

6. **TestHistogram_Time_SetsSpanError** — create an active span, call Time with a failing function, assert the span has status Error and recorded the error.

7. **TestHistogram_Time_NoSpan_DoesNotPanic** — call Time without an active span and a failing function, assert no panic (graceful degradation).

8. **TestDurationIn_Conversions** — table-driven test for unit conversions: "ms", "s", "" (default seconds). Tests indirectly via `Histogram.Time` since `durationIn` is unexported.

9. **TestNewGauge_ReturnsWorkingGauge** — create gauge via NewGauge, call Record with value 17.5, collect metrics, assert Gauge[float64] data point has value 17.5.

10. **TestNewUpDownCounter_ReturnsWorkingUpDownCounter** — create via NewUpDownCounter, call Add(ctx, 5), collect, assert value is 5.

11. **TestUpDownCounter_IncrementDecrement** — call Increment 3 times, Decrement 1 time, assert net value is 2.

12. **TestUpDownCounter_Increment_WithAttributes** — call Increment with attributes, assert attributes appear on data point.

### `trace_test.go`

Trace tests use in-memory span exporter.

13. **TestTrace_CreatesAndEndsSpan** — call Trace with a no-op function, assert exactly 1 span was created with the correct name.

14. **TestTrace_PassesContextWithSpan** — inside fn, extract span from ctx, assert it matches the span argument.

15. **TestTrace_PropagatesError** — call Trace with a function returning an error, assert Trace returns the same error.

16. **TestTrace_SetsSpanErrorOnFailure** — call Trace with a failing function, assert span status is Error and error is recorded.

17. **TestTrace_DoesNotSetErrorOnSuccess** — call Trace with a successful function, assert span status is Unset.

18. **TestTrace_AppliesSpanStartOptions** — pass `trace.WithAttributes(...)`, assert the span has those attributes.

19. **TestTrace_NestsChildSpans** — call Trace inside Trace, assert child span's parent is the outer span.

### `oteltest/oteltest_test.go`

20. **TestSetup_ReturnsWorkingTracer** — create span via harness.Tracer, assert harness.Spans() returns it.

21. **TestSetup_ReturnsWorkingMeter** — create counter via harness.Meter, record value, assert harness.Metrics(t) contains it.

22. **TestFindMetric_ReturnsNilWhenNotFound** — assert FindMetric returns nil for non-existent metric name.

23. **TestMetricAttrs_ConvertsToMap** — create attribute set, assert MetricAttrs returns correct map.

## Todo List

### Phase 1: Test Helpers (oteltest/)
- [x] Create `oteltest/oteltest.go` with `Setup`, `Harness`, `FindMetric`, `MetricAttrs`
- [x] Write `TestSetup_ReturnsWorkingTracer` in `oteltest/oteltest_test.go`
- [x] Write `TestSetup_ReturnsWorkingMeter` in `oteltest/oteltest_test.go`
- [x] Write `TestFindMetric_ReturnsNilWhenNotFound` in `oteltest/oteltest_test.go`
- [x] Write `TestMetricAttrs_ConvertsToMap` in `oteltest/oteltest_test.go`
- [x] Run `go test ./oteltest/...` — all green

### Phase 2: Trace Convenience
- [x] Write `TestTrace_CreatesAndEndsSpan` in `trace_test.go` (RED)
- [x] Write `TestTrace_PassesContextWithSpan` in `trace_test.go` (RED)
- [x] Write `TestTrace_PropagatesError` in `trace_test.go` (RED)
- [x] Write `TestTrace_SetsSpanErrorOnFailure` in `trace_test.go` (RED)
- [x] Write `TestTrace_DoesNotSetErrorOnSuccess` in `trace_test.go` (RED)
- [x] Write `TestTrace_AppliesSpanStartOptions` in `trace_test.go` (RED)
- [x] Write `TestTrace_NestsChildSpans` in `trace_test.go` (RED)
- [x] Implement `Trace()` in `trace.go` (GREEN)
- [x] Run `go test -run TestTrace` — all green

### Phase 3: Metric Wrappers — Counter
- [x] Write `TestNewCounter_ReturnsWorkingCounter` in `metric_test.go` (RED)
- [x] Write `TestCounter_MultipleAdds` in `metric_test.go` (RED)
- [x] Implement `Counter` + `NewCounter` in `metric.go` (GREEN)
- [x] Run `go test -run TestCounter` — all green

### Phase 4: Metric Wrappers — Histogram
- [x] Write `TestNewHistogram_ReturnsWorkingHistogram` in `metric_test.go` (RED)
- [x] Write `TestHistogram_Time_RecordsDuration` in `metric_test.go` (RED)
- [x] Write `TestHistogram_Time_ReturnsError` in `metric_test.go` (RED)
- [x] Write `TestHistogram_Time_SetsSpanError` in `metric_test.go` (RED)
- [x] Write `TestHistogram_Time_NoSpan_DoesNotPanic` in `metric_test.go` (RED)
- [x] Write `TestDurationIn_Conversions` in `metric_test.go` (RED)
- [x] Implement `Histogram` + `NewHistogram` + `durationIn` + `extractHistogramUnit` in `metric.go` (GREEN)
- [x] Run `go test -run "TestHistogram|TestDurationIn"` — all green

### Phase 5: Metric Wrappers — Gauge & UpDownCounter
- [x] Write `TestNewGauge_ReturnsWorkingGauge` in `metric_test.go` (RED)
- [x] Implement `Gauge` + `NewGauge` in `metric.go` (GREEN)
- [x] Write `TestNewUpDownCounter_ReturnsWorkingUpDownCounter` in `metric_test.go` (RED)
- [x] Write `TestUpDownCounter_IncrementDecrement` in `metric_test.go` (RED)
- [x] Write `TestUpDownCounter_Increment_WithAttributes` in `metric_test.go` (RED)
- [x] Implement `UpDownCounter` + `NewUpDownCounter` in `metric.go` (GREEN)
- [x] Run `go test -run "TestGauge|TestUpDownCounter"` — all green

### Phase 6: Final Verification
- [x] Run full test suite: `go test ./...`
- [x] Run `go vet ./...`

## Verification Summary

Fact-checked against the implemented codebase on 2026-03-11.

- **Total claims checked**: 42 (type names, function signatures, code snippets, test descriptions, file paths, design claims)
- **Confirmed**: 38
- **Corrected**: 4
  - `Histogram.Time()` code snippet: updated nested `if err != nil { if span := ...` to match refactored single-condition `if span := ...; err != nil && span.IsRecording()`
  - `TestDurationIn_Conversions` description: changed "all unit conversions: ns, us, ms, s, min, h, '', unknown" to actual 3 test cases ("ms", "s", "")
  - `extractHistogramUnit` comment: removed extra explanation lines to match actual simplified godoc
  - `Increment`/`Decrement` godoc: changed "adds n (default 1)" to "adds 1" to match actual signatures (no `n` parameter)
- **Unverifiable**: 0
