package otel

import (
	"fmt"
	"net/http"
	"time"

	gotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// Middleware holds the tracer and pre-registered HTTP metric instruments.
// Construct one with NewMiddleware and reuse it across the application.
type Middleware struct {
	tracer          trace.Tracer
	requestCount    metric.Int64Counter
	requestDuration metric.Float64Histogram
	activeRequests  metric.Int64UpDownCounter
}

// NewMiddleware creates a Middleware from explicit tracer and meter instances
// (as returned by Setup). It registers the HTTP metric instruments once.
func NewMiddleware(tracer trace.Tracer, meter metric.Meter) (*Middleware, error) {
	reqCount, err := meter.Int64Counter("http.server.request.count",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("http.server.request.count: %w", err)
	}

	reqDur, err := meter.Float64Histogram("http.server.request.duration",
		metric.WithDescription("HTTP request latency in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("http.server.request.duration: %w", err)
	}

	active, err := meter.Int64UpDownCounter("http.server.active_requests",
		metric.WithDescription("Number of in-flight HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("http.server.active_requests: %w", err)
	}

	return &Middleware{
		tracer:          tracer,
		requestCount:    reqCount,
		requestDuration: reqDur,
		activeRequests:  active,
	}, nil
}

// Wrap returns an http.Handler that automatically instruments every route
// registered on next. The span name and http.route label are read from
// r.Pattern (set by http.ServeMux in Go 1.22+ after routing).
//
// Because r.Pattern is only known after the mux has matched the route, the
// span name and metric labels are finalised after next.ServeHTTP returns.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := gotel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Start the span with a temporary name; we'll set the final name after routing.
		ctx, span := m.tracer.Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
			),
		)
		defer span.End()

		sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		// Inject the span context. r.WithContext returns a shallow copy; after
		// next.ServeHTTP returns, rWithCtx.Pattern will be set by http.ServeMux.
		rWithCtx := r.WithContext(ctx)

		m.activeRequests.Add(ctx, 1, metric.WithAttributes(semconv.HTTPRequestMethodKey.String(r.Method)))
		defer m.activeRequests.Add(ctx, -1, metric.WithAttributes(semconv.HTTPRequestMethodKey.String(r.Method)))

		start := time.Now()
		next.ServeHTTP(sr, rWithCtx)
		dur := time.Since(start).Seconds()

		// r.Pattern is set on rWithCtx by http.ServeMux during routing.
		// Fall back to the raw URL path if this handler is not wrapped in a ServeMux.
		pattern := rWithCtx.Pattern
		if pattern == "" {
			pattern = r.URL.Path
		}

		span.SetName(fmt.Sprintf("%s %s", r.Method, pattern))

		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.HTTPRouteKey.String(pattern),
			semconv.HTTPResponseStatusCode(sr.statusCode),
		}
		m.requestCount.Add(ctx, 1, metric.WithAttributes(attrs...))
		m.requestDuration.Record(ctx, dur, metric.WithAttributes(attrs...))

		span.SetAttributes(semconv.HTTPResponseStatusCode(sr.statusCode))
		if sr.statusCode >= 500 {
			span.SetStatus(codes.Error, http.StatusText(sr.statusCode))
		}
	})
}

// statusRecorder captures the HTTP response status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}
