package oteltest_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/bitsmithy/go-otel/oteltest"
)

func TestSetup_ReturnsWorkingTracer(t *testing.T) {
	h := oteltest.Setup(t)

	_, span := h.Tracer.Start(context.Background(), "test-span")
	span.End()

	spans := h.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "test-span" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "test-span")
	}
}

func TestSetup_ReturnsWorkingMeter(t *testing.T) {
	h := oteltest.Setup(t)

	counter, err := h.Meter.Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("creating counter: %v", err)
	}
	counter.Add(context.Background(), 7)

	rm := h.Metrics(t)
	m := oteltest.FindMetric(rm, "test.counter")
	if m == nil {
		t.Fatal("metric test.counter not found")
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 7 {
		t.Errorf("counter value = %d, want 7", sum.DataPoints[0].Value)
	}
}

func TestFindMetric_ReturnsNilWhenNotFound(t *testing.T) {
	h := oteltest.Setup(t)
	rm := h.Metrics(t)

	got := oteltest.FindMetric(rm, "nonexistent.metric")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestMetricAttrs_ConvertsToMap(t *testing.T) {
	attrs := attribute.NewSet(
		attribute.String("region", "us-east-1"),
		attribute.String("env", "prod"),
	)

	got := oteltest.MetricAttrs(attrs)

	if got["region"] != "us-east-1" {
		t.Errorf("region = %q, want %q", got["region"], "us-east-1")
	}
	if got["env"] != "prod" {
		t.Errorf("env = %q, want %q", got["env"], "prod")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d", len(got))
	}
}
