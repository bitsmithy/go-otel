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
