package otel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	otel "github.com/bitsmithy/go-otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTestMeter(t *testing.T) (metric.Meter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return mp.Meter("test"), reader
}

// --- Counter tests ---

func TestNewCounter_ReturnsWorkingCounter(t *testing.T) {
	meter, reader := newTestMeter(t)

	counter, err := otel.NewCounter(meter, "orders.placed", metric.WithUnit("{order}"))
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}

	counter.Add(context.Background(), 5, attribute.String("method", "card"))

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "orders.placed")
	if m == nil {
		t.Fatal("metric orders.placed not found")
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(sum.DataPoints))
	}
	if sum.DataPoints[0].Value != 5 {
		t.Errorf("counter value = %d, want 5", sum.DataPoints[0].Value)
	}
	attrs := make(map[string]string)
	for _, kv := range sum.DataPoints[0].Attributes.ToSlice() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["method"] != "card" {
		t.Errorf("attribute method = %q, want %q", attrs["method"], "card")
	}
}

func TestCounter_MultipleAdds(t *testing.T) {
	meter, reader := newTestMeter(t)

	counter, err := otel.NewCounter(meter, "events.total")
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}

	ctx := context.Background()
	counter.Add(ctx, 1)
	counter.Add(ctx, 1)
	counter.Add(ctx, 1)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "events.total")
	if m == nil {
		t.Fatal("metric not found")
	}

	sum := m.Data.(metricdata.Sum[int64])
	if sum.DataPoints[0].Value != 3 {
		t.Errorf("counter value = %d, want 3", sum.DataPoints[0].Value)
	}
}

// --- Histogram tests ---

func TestNewHistogram_ReturnsWorkingHistogram(t *testing.T) {
	meter, reader := newTestMeter(t)

	hist, err := otel.NewHistogram(meter, "request.size", metric.WithUnit("By"))
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}

	hist.Record(context.Background(), 42.0)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "request.size")
	if m == nil {
		t.Fatal("metric not found")
	}

	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("expected Histogram[float64], got %T", m.Data)
	}
	if len(h.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(h.DataPoints))
	}
	if h.DataPoints[0].Count != 1 {
		t.Errorf("histogram count = %d, want 1", h.DataPoints[0].Count)
	}
}

func TestHistogram_Time_RecordsDuration(t *testing.T) {
	meter, reader := newTestMeter(t)

	hist, err := otel.NewHistogram(meter, "op.duration", metric.WithUnit("ms"))
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}

	err = hist.Time(context.Background(), func(ctx context.Context) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("Time returned error: %v", err)
	}

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "op.duration")
	if m == nil {
		t.Fatal("metric not found")
	}

	h := m.Data.(metricdata.Histogram[float64])
	if h.DataPoints[0].Count != 1 {
		t.Errorf("histogram count = %d, want 1", h.DataPoints[0].Count)
	}
	if h.DataPoints[0].Sum < 10 {
		t.Errorf("histogram sum = %f, want >= 10 (ms)", h.DataPoints[0].Sum)
	}
}

func TestHistogram_Time_ReturnsError(t *testing.T) {
	meter, _ := newTestMeter(t)

	hist, err := otel.NewHistogram(meter, "op.duration", metric.WithUnit("ms"))
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}

	want := errors.New("timeout")
	got := hist.Time(context.Background(), func(ctx context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Errorf("got error %v, want %v", got, want)
	}
}

func TestHistogram_Time_SetsSpanError(t *testing.T) {
	meter, _ := newTestMeter(t)

	hist, err := otel.NewHistogram(meter, "op.duration", metric.WithUnit("ms"))
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "parent")

	_ = hist.Time(ctx, func(ctx context.Context) error {
		return errors.New("db connection failed")
	})
	span.End()

	recorded := sr.Ended()
	if len(recorded) != 1 {
		t.Fatalf("expected 1 span, got %d", len(recorded))
	}

	stub := tracetest.SpanStubFromReadOnlySpan(recorded[0])
	if stub.Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error", stub.Status.Code)
	}

	hasException := false
	for _, ev := range stub.Events {
		if ev.Name == "exception" {
			hasException = true
			break
		}
	}
	if !hasException {
		t.Error("span does not have an exception event")
	}
}

func TestHistogram_Time_NoSpan_DoesNotPanic(t *testing.T) {
	meter, _ := newTestMeter(t)

	hist, err := otel.NewHistogram(meter, "op.duration")
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}

	// context.Background() has no active span — should not panic
	err = hist.Time(context.Background(), func(ctx context.Context) error {
		return errors.New("fail")
	})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestDurationIn_Conversions(t *testing.T) {
	// durationIn is unexported, so we test it indirectly through Histogram.Time.
	// We create histograms with different units and verify the recorded values.
	tests := []struct {
		unit    string
		sleep   time.Duration
		wantMin float64
	}{
		{"ms", 10 * time.Millisecond, 10},
		{"s", 10 * time.Millisecond, 0.01},
		{"", 10 * time.Millisecond, 0.01}, // default is seconds
	}

	for _, tt := range tests {
		t.Run("unit="+tt.unit, func(t *testing.T) {
			meter, reader := newTestMeter(t)

			var opts []metric.Float64HistogramOption
			if tt.unit != "" {
				opts = append(opts, metric.WithUnit(tt.unit))
			}
			hist, err := otel.NewHistogram(meter, "test.duration", opts...)
			if err != nil {
				t.Fatalf("NewHistogram: %v", err)
			}

			_ = hist.Time(context.Background(), func(ctx context.Context) error {
				time.Sleep(tt.sleep)
				return nil
			})

			rm := collectMetrics(t, reader)
			m := findMetric(rm, "test.duration")
			if m == nil {
				t.Fatal("metric not found")
			}

			h := m.Data.(metricdata.Histogram[float64])
			if h.DataPoints[0].Sum < tt.wantMin {
				t.Errorf("sum = %f, want >= %f (unit=%q)", h.DataPoints[0].Sum, tt.wantMin, tt.unit)
			}
		})
	}
}

// --- Gauge tests ---

func TestNewGauge_ReturnsWorkingGauge(t *testing.T) {
	meter, reader := newTestMeter(t)

	gauge, err := otel.NewGauge(meter, "cpu.temperature", metric.WithUnit("Cel"))
	if err != nil {
		t.Fatalf("NewGauge: %v", err)
	}

	gauge.Record(context.Background(), 17.5)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "cpu.temperature")
	if m == nil {
		t.Fatal("metric not found")
	}

	g, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("expected Gauge[float64], got %T", m.Data)
	}
	if len(g.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(g.DataPoints))
	}
	if g.DataPoints[0].Value != 17.5 {
		t.Errorf("gauge value = %f, want 17.5", g.DataPoints[0].Value)
	}
}

// --- UpDownCounter tests ---

func TestNewUpDownCounter_ReturnsWorkingUpDownCounter(t *testing.T) {
	meter, reader := newTestMeter(t)

	udc, err := otel.NewUpDownCounter(meter, "db.connections")
	if err != nil {
		t.Fatalf("NewUpDownCounter: %v", err)
	}

	udc.Add(context.Background(), 5)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "db.connections")
	if m == nil {
		t.Fatal("metric not found")
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if sum.DataPoints[0].Value != 5 {
		t.Errorf("value = %d, want 5", sum.DataPoints[0].Value)
	}
}

func TestUpDownCounter_IncrementDecrement(t *testing.T) {
	meter, reader := newTestMeter(t)

	udc, err := otel.NewUpDownCounter(meter, "queue.size")
	if err != nil {
		t.Fatalf("NewUpDownCounter: %v", err)
	}

	ctx := context.Background()
	udc.Increment(ctx)
	udc.Increment(ctx)
	udc.Increment(ctx)
	udc.Decrement(ctx)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "queue.size")
	if m == nil {
		t.Fatal("metric not found")
	}

	sum := m.Data.(metricdata.Sum[int64])
	if sum.DataPoints[0].Value != 2 {
		t.Errorf("value = %d, want 2", sum.DataPoints[0].Value)
	}
}

func TestUpDownCounter_Increment_WithAttributes(t *testing.T) {
	meter, reader := newTestMeter(t)

	udc, err := otel.NewUpDownCounter(meter, "pool.active")
	if err != nil {
		t.Fatalf("NewUpDownCounter: %v", err)
	}

	udc.Increment(context.Background(), attribute.String("pool", "primary"))

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "pool.active")
	if m == nil {
		t.Fatal("metric not found")
	}

	sum := m.Data.(metricdata.Sum[int64])
	attrs := make(map[string]string)
	for _, kv := range sum.DataPoints[0].Attributes.ToSlice() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["pool"] != "primary" {
		t.Errorf("attribute pool = %q, want %q", attrs["pool"], "primary")
	}
}

