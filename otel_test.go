package otel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	otel "github.com/bitsmithy/go-otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	gotrace "go.opentelemetry.io/otel/trace"
)

// cancelledShutdownCtx returns an already-cancelled context suitable for
// passing to shutdown() so OTLP exporters skip flushing (no collector in tests).
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// doSetup calls Setup with a live background context (so exporter init
// succeeds) but registers a t.Cleanup that shuts down with a cancelled
// context so no actual export is attempted.
func doSetup(t *testing.T, cfg otel.Config) (shutdown func(context.Context) error) {
	t.Helper()
	shutdown, _, _, _, err := otel.Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(cancelledCtx()) })
	return shutdown
}

func TestSetup_NoSchemaConflict(t *testing.T) {
	shutdown := doSetup(t, otel.Config{ServiceName: "test-svc"})
	_ = shutdown(cancelledCtx())
}

func TestSetup_ReturnsNonNilLogger(t *testing.T) {
	doSetup(t, otel.Config{ServiceName: "test-svc"})

	_, log, _, _, err := otel.Setup(context.Background(), otel.Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	if log == nil {
		t.Error("Setup returned nil logger")
	}
}

func TestSetup_ReturnsNonNilTracerAndMeter(t *testing.T) {
	_, _, tracer, meter, err := otel.Setup(context.Background(), otel.Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Setup returned error: %v", err)
	}
	if tracer == nil {
		t.Error("Setup returned nil tracer")
	}
	if meter == nil {
		t.Error("Setup returned nil meter")
	}
}

func TestSetup_DefaultsServiceName(t *testing.T) {
	// Config{} — no ServiceName — must succeed and use binaryName() default.
	_, _, _, _, err := otel.Setup(context.Background(), otel.Config{})
	if err != nil {
		t.Fatalf("Setup with empty Config returned error: %v", err)
	}
}

func TestSetup_EndpointOverride(t *testing.T) {
	// Setup must succeed even with a non-reachable endpoint — OTLP exporters
	// connect lazily, so no network call is made during initialisation.
	shutdown, _, _, _, err := otel.Setup(context.Background(), otel.Config{
		ServiceName: "test-svc",
		Endpoint:    "http://localhost:19999", // nothing listening here
	})
	if err != nil {
		t.Fatalf("Setup with Endpoint override returned error: %v", err)
	}
	_ = shutdown(cancelledCtx())
}

func TestSetup_EnvHeadersSentToCollector(t *testing.T) {
	var receivedAuth atomic.Value

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			receivedAuth.Store(auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer test-token")

	ctx := context.Background()
	shutdown, _, tracer, _, err := otel.Setup(ctx, otel.Config{
		ServiceName: "test-svc",
		Endpoint:    collector.URL,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	_, span := tracer.Start(ctx, "test-op")
	span.End()

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got, _ := receivedAuth.Load().(string)
	if got != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
	}
}

func TestDetachedContext_NotCancelledWhenParentIs(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	got := otel.DetachedContext(parent)

	select {
	case <-got.Done():
		t.Error("DetachedContext returned a cancelled context, want a live one")
	default:
		// pass
	}
}

func TestDetachedContext_PreservesSpan(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))

	parent, span := tp.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	got := otel.DetachedContext(parent)

	gotSpan := gotrace.SpanFromContext(got)
	if gotSpan.SpanContext().SpanID() != span.SpanContext().SpanID() {
		t.Errorf("span_id = %s, want %s",
			gotSpan.SpanContext().SpanID(),
			span.SpanContext().SpanID(),
		)
	}
}
