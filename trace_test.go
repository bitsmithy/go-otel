package otel_test

import (
	"context"
	"errors"
	"testing"

	otel "github.com/bitsmithy/go-otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTestTracer(t *testing.T) (trace.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test"), sr
}

func TestTrace_CreatesAndEndsSpan(t *testing.T) {
	tracer, sr := newTestTracer(t)

	err := otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Trace returned error: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "test.op" {
		t.Errorf("span name = %q, want %q", spans[0].Name(), "test.op")
	}
}

func TestTrace_PassesContextWithSpan(t *testing.T) {
	tracer, _ := newTestTracer(t)

	err := otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		ctxSpan := trace.SpanFromContext(ctx)
		if ctxSpan.SpanContext().SpanID() != span.SpanContext().SpanID() {
			t.Errorf("context span ID = %s, want %s", ctxSpan.SpanContext().SpanID(), span.SpanContext().SpanID())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Trace returned error: %v", err)
	}
}

func TestTrace_PropagatesError(t *testing.T) {
	tracer, _ := newTestTracer(t)
	want := errors.New("boom")

	got := otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Errorf("got error %v, want %v", got, want)
	}
}

func TestTrace_SetsSpanErrorOnFailure(t *testing.T) {
	tracer, sr := newTestTracer(t)

	_ = otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		return errors.New("something failed")
	})

	span := tracetest.SpanStubFromReadOnlySpan(sr.Ended()[0])
	if span.Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error", span.Status.Code)
	}
	if span.Status.Description != "something failed" {
		t.Errorf("span status description = %q, want %q", span.Status.Description, "something failed")
	}

	hasError := false
	for _, ev := range span.Events {
		if ev.Name == "exception" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("span does not have an error event recorded")
	}
}

func TestTrace_DoesNotSetErrorOnSuccess(t *testing.T) {
	tracer, sr := newTestTracer(t)

	_ = otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		return nil
	})

	span := tracetest.SpanStubFromReadOnlySpan(sr.Ended()[0])
	if span.Status.Code != codes.Unset {
		t.Errorf("span status = %v, want Unset", span.Status.Code)
	}
}

func TestTrace_AppliesSpanStartOptions(t *testing.T) {
	tracer, sr := newTestTracer(t)

	_ = otel.Trace(context.Background(), tracer, "test.op", func(ctx context.Context, span trace.Span) error {
		return nil
	}, trace.WithAttributes(attribute.String("key", "value")))

	span := tracetest.SpanStubFromReadOnlySpan(sr.Ended()[0])
	found := false
	for _, a := range span.Attributes {
		if string(a.Key) == "key" && a.Value.AsString() == "value" {
			found = true
			break
		}
	}
	if !found {
		t.Error("span does not have the expected attribute key=value")
	}
}

func TestTrace_NestsChildSpans(t *testing.T) {
	tracer, sr := newTestTracer(t)

	_ = otel.Trace(context.Background(), tracer, "parent", func(ctx context.Context, parentSpan trace.Span) error {
		return otel.Trace(ctx, tracer, "child", func(ctx context.Context, childSpan trace.Span) error {
			return nil
		})
	})

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	// Child ends first, so spans[0] is "child" and spans[1] is "parent"
	child := tracetest.SpanStubFromReadOnlySpan(spans[0])
	parent := tracetest.SpanStubFromReadOnlySpan(spans[1])

	if child.Name != "child" || parent.Name != "parent" {
		t.Fatalf("unexpected span order: %q, %q", spans[0].Name(), spans[1].Name())
	}
	if child.Parent.SpanID() != parent.SpanContext.SpanID() {
		t.Errorf("child parent span ID = %s, want %s", child.Parent.SpanID(), parent.SpanContext.SpanID())
	}
	if child.SpanContext.TraceID() != parent.SpanContext.TraceID() {
		t.Errorf("child trace ID = %s, want %s", child.SpanContext.TraceID(), parent.SpanContext.TraceID())
	}
}
