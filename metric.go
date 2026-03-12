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
