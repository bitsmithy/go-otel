package otel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	otel "github.com/bitsmithy/go-otel"
	gotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestMiddleware creates a Middleware backed by in-memory exporters and
// returns it along with the span recorder and metric reader. It also sets
// the global OTel providers so that code calling gotel.GetTextMapPropagator()
// works correctly.
func newTestMiddleware(t *testing.T) (*otel.Middleware, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	gotel.SetTracerProvider(tp)
	gotel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	mw, err := otel.NewMiddleware(
		tp.Tracer("test-svc"),
		mp.Meter("test-svc"),
	)
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}

	return mw, sr, reader
}

// collectMetrics collects all metrics from the reader.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return rm
}

// findMetric searches collected metrics for one with the given name.
func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// serveOne sends one request through the middleware-wrapped handler.
func serveOne(mw *otel.Middleware, pattern string, handler http.HandlerFunc, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	mw.Wrap(mux).ServeHTTP(rec, req)
	return rec
}

// --- Span tests ---

func TestMiddleware_CreatesSpan(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	serveOne(mw, "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, http.MethodGet, "/test")

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "GET /test" {
		t.Errorf("span name = %q, want %q", spans[0].Name(), "GET /test")
	}
}

func TestMiddleware_SpanAttributes(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	req.Header.Set("User-Agent", "test-agent")
	rec := httptest.NewRecorder()
	mw.Wrap(mux).ServeHTTP(rec, req)

	span := sr.Ended()[0]
	attrs := make(map[string]string)
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.Emit()
	}

	want := map[string]string{
		"http.request.method":       "POST",
		"url.path":                  "/items",
		"user_agent.original":       "test-agent",
		"http.response.status_code": "200",
	}
	for key, wantVal := range want {
		if got := attrs[key]; got != wantVal {
			t.Errorf("attribute %q = %q, want %q", key, got, wantVal)
		}
	}
}

func TestMiddleware_500SetsErrorStatus(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	serveOne(mw, "/fail", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, http.MethodGet, "/fail")

	span := sr.Ended()[0]
	if span.Status().Code != 1 { // codes.Error = 1
		t.Errorf("span status code = %d, want 1 (Error)", span.Status().Code)
	}
}

func TestMiddleware_4xxDoesNotSetErrorStatus(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	serveOne(mw, "/notfound", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}, http.MethodGet, "/notfound")

	span := sr.Ended()[0]
	if span.Status().Code != 0 { // codes.Unset = 0
		t.Errorf("span status code = %d, want 0 (Unset) for 404", span.Status().Code)
	}
}

// --- Metric tests ---

func TestMiddleware_RequestCountMetric(t *testing.T) {
	mw, _, reader := newTestMiddleware(t)

	for range 3 {
		serveOne(mw, "/count", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}, http.MethodGet, "/count")
	}

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "http.server.request.count")
	if m == nil {
		t.Fatal("metric http.server.request.count not found")
		return
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
		return
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(sum.DataPoints))
		return
	}
	if sum.DataPoints[0].Value != 3 {
		t.Errorf("request count = %d, want 3", sum.DataPoints[0].Value)
	}
}

func TestMiddleware_RequestDurationMetric(t *testing.T) {
	mw, _, reader := newTestMiddleware(t)

	serveOne(mw, "/dur", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, http.MethodGet, "/dur")

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "http.server.request.duration")
	if m == nil {
		t.Fatal("metric http.server.request.duration not found")
		return
	}

	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("expected Histogram[float64], got %T", m.Data)
		return
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(hist.DataPoints))
		return
	}
	if hist.DataPoints[0].Count != 1 {
		t.Errorf("histogram count = %d, want 1", hist.DataPoints[0].Count)
	}
}

func TestMiddleware_ActiveRequestsMetric(t *testing.T) {
	mw, _, reader := newTestMiddleware(t)

	var inFlightRM metricdata.ResourceMetrics
	mux := http.NewServeMux()
	mux.HandleFunc("/active", func(w http.ResponseWriter, r *http.Request) {
		if err := reader.Collect(r.Context(), &inFlightRM); err != nil {
			t.Errorf("collecting in-flight metrics: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/active", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(mux).ServeHTTP(rec, req)

	// In-flight: active_requests should be 1.
	m := findMetric(inFlightRM, "http.server.active_requests")
	if m == nil {
		t.Fatal("metric http.server.active_requests not found during request")
		return
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
		return
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Errorf("active requests during handler = %v, want 1", sum.DataPoints)
	}

	// After: active_requests should be 0.
	afterRM := collectMetrics(t, reader)
	m = findMetric(afterRM, "http.server.active_requests")
	if m == nil {
		t.Fatal("metric http.server.active_requests not found after request")
		return
	}
	sum, ok = m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
		return
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 0 {
		t.Errorf("active requests after handler = %v, want 0", sum.DataPoints)
	}
}

func TestMiddleware_MetricLabels(t *testing.T) {
	mw, _, reader := newTestMiddleware(t)

	serveOne(mw, "/labeled", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}, http.MethodPost, "/labeled")

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "http.server.request.count")
	if m == nil {
		t.Fatal("metric not found")
		return
	}

	sum := m.Data.(metricdata.Sum[int64])
	attrs := make(map[string]string)
	for _, kv := range sum.DataPoints[0].Attributes.ToSlice() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}

	want := map[string]string{
		"http.request.method":       "POST",
		"http.route":                "/labeled",
		"http.response.status_code": "201",
	}
	for key, wantVal := range want {
		if got := attrs[key]; got != wantVal {
			t.Errorf("metric attribute %q = %q, want %q", key, got, wantVal)
		}
	}
}

// --- Context propagation tests ---

func TestMiddleware_PropagatesContext(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/ctx", func(w http.ResponseWriter, r *http.Request) {
		_, child := gotel.Tracer("test").Start(r.Context(), "child-op")
		child.End()
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/ctx", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(mux).ServeHTTP(rec, req)

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	var parent, child *tracetest.SpanStub
	for i := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(spans[i])
		if stub.Name == "child-op" {
			child = &stub
		} else {
			parent = &stub
		}
	}
	if child == nil || parent == nil {
		t.Fatal("could not find both parent and child spans")
		return
	}
	if child.Parent.TraceID() != parent.SpanContext.TraceID() {
		t.Errorf("child trace ID %s != parent trace ID %s", child.Parent.TraceID(), parent.SpanContext.TraceID())
	}
	if child.Parent.SpanID() != parent.SpanContext.SpanID() {
		t.Errorf("child parent span ID %s != parent span ID %s", child.Parent.SpanID(), parent.SpanContext.SpanID())
	}
}

func TestMiddleware_ExtractsPropagatedTrace(t *testing.T) {
	mw, sr, _ := newTestMiddleware(t)

	// Create upstream span and inject its context into headers.
	upstreamCtx, upstreamSpan := gotel.Tracer("upstream").Start(context.Background(), "upstream-op")
	upstreamSpan.End()

	headers := make(http.Header)
	gotel.GetTextMapPropagator().Inject(upstreamCtx, propagation.HeaderCarrier(headers))

	mux := http.NewServeMux()
	mux.HandleFunc("/extract", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/extract", nil)
	for k, v := range headers {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	mw.Wrap(mux).ServeHTTP(rec, req)

	var serverSpan *tracetest.SpanStub
	for _, s := range sr.Ended() {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		if stub.Name == "GET /extract" {
			serverSpan = &stub
			break
		}
	}
	if serverSpan == nil {
		t.Fatal("server span not found")
		return
	}

	upstreamSC := upstreamSpan.SpanContext()
	if serverSpan.Parent.TraceID() != upstreamSC.TraceID() {
		t.Errorf("server span trace ID %s != upstream trace ID %s",
			serverSpan.Parent.TraceID(), upstreamSC.TraceID())
	}
	if serverSpan.Parent.SpanID() != upstreamSC.SpanID() {
		t.Errorf("server span parent ID %s != upstream span ID %s",
			serverSpan.Parent.SpanID(), upstreamSC.SpanID())
	}
}

func TestMiddleware_UsesPattern(t *testing.T) {
	mw, _, reader := newTestMiddleware(t)

	// Register with a parameterised pattern — r.Pattern will be "/items/{id}"
	// but the actual URL path will be "/items/42".
	serveOne(mw, "/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, http.MethodGet, "/items/42")

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "http.server.request.count")
	if m == nil {
		t.Fatal("metric not found")
		return
	}

	sum := m.Data.(metricdata.Sum[int64])
	attrs := make(map[string]string)
	for _, kv := range sum.DataPoints[0].Attributes.ToSlice() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}

	// The route label must be the pattern, not the concrete path.
	if got := attrs["http.route"]; got != "/items/{id}" {
		t.Errorf("http.route = %q, want %q", got, "/items/{id}")
	}
}
