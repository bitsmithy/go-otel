package otel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	otel "github.com/bitsmithy/go-otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTraceContext starts a test span and returns the context carrying it,
// along with the expected trace_id and span_id strings.
func newTraceContext(t *testing.T) (ctx context.Context, wantTraceID, wantSpanID string) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()
}

// newTraceHandler creates a TraceHandler writing JSON to buf.
func newTraceHandler(buf *bytes.Buffer) *otel.TraceHandler {
	return &otel.TraceHandler{Handler: slog.NewJSONHandler(buf, nil)}
}

// --- TraceHandler tests ---

func TestTraceHandler_InjectsTraceAndSpanID(t *testing.T) {
	ctx, wantTraceID, wantSpanID := newTraceContext(t)

	var buf bytes.Buffer
	log := slog.New(newTraceHandler(&buf))
	log.InfoContext(ctx, "hello")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, buf.String())
	}
	if got["trace_id"] != wantTraceID {
		t.Errorf("trace_id = %q, want %q", got["trace_id"], wantTraceID)
	}
	if got["span_id"] != wantSpanID {
		t.Errorf("span_id = %q, want %q", got["span_id"], wantSpanID)
	}
}

func TestTraceHandler_NoSpan(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newTraceHandler(&buf))
	log.InfoContext(context.Background(), "hello")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, buf.String())
	}
	if _, ok := got["trace_id"]; ok {
		t.Error("expected no trace_id without active span")
	}
	if _, ok := got["span_id"]; ok {
		t.Error("expected no span_id without active span")
	}
}

func TestTraceHandler_WithAttrs_Preserved(t *testing.T) {
	var buf bytes.Buffer
	h := newTraceHandler(&buf).WithAttrs([]slog.Attr{slog.String("service", "test")})
	log := slog.New(h)
	log.InfoContext(context.Background(), "hello")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if got["service"] != "test" {
		t.Errorf("service attr = %q, want %q", got["service"], "test")
	}
}

func TestTraceHandler_WithGroup_Preserved(t *testing.T) {
	var buf bytes.Buffer
	h := newTraceHandler(&buf).WithGroup("grp")
	log := slog.New(h)
	log.InfoContext(context.Background(), "hello", slog.String("k", "v"))

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	grp, ok := got["grp"].(map[string]any)
	if !ok {
		t.Fatalf("expected group 'grp' in output, got: %v", got)
	}
	if grp["k"] != "v" {
		t.Errorf("grp.k = %q, want %q", grp["k"], "v")
	}
}

// --- FanoutHandler tests ---

// countingHandler records how many records it has handled.
type countingHandler struct{ count int }

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool  { return true }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error { h.count++; return nil }
func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler               { return h }

// errorHandler always returns a fixed error from Handle.
type errorHandler struct{ err error }

func (h *errorHandler) Enabled(_ context.Context, _ slog.Level) bool  { return true }
func (h *errorHandler) Handle(_ context.Context, _ slog.Record) error { return h.err }
func (h *errorHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return h }
func (h *errorHandler) WithGroup(_ string) slog.Handler               { return h }

func TestFanoutHandler_DeliversToAllSinks(t *testing.T) {
	a, b := &countingHandler{}, &countingHandler{}
	log := slog.New(otel.FanoutHandler{a, b})
	log.InfoContext(context.Background(), "hello")

	if a.count != 1 {
		t.Errorf("sink A count = %d, want 1", a.count)
	}
	if b.count != 1 {
		t.Errorf("sink B count = %d, want 1", b.count)
	}
}

func TestFanoutHandler_EnabledIfAnyEnabled(t *testing.T) {
	disabled := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError})
	enabled := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	f := otel.FanoutHandler{disabled, enabled}

	if !f.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("FanoutHandler.Enabled = false, want true (at least one handler is enabled)")
	}
}

func TestFanoutHandler_ReturnsHandlerErrors(t *testing.T) {
	sentinel := errors.New("handler error")
	f := otel.FanoutHandler{&errorHandler{err: sentinel}}

	var zeroRecord slog.Record
	err := f.Handle(context.Background(), zeroRecord)
	if !errors.Is(err, sentinel) {
		t.Errorf("FanoutHandler.Handle error = %v, want %v", err, sentinel)
	}
}

func TestFanoutHandler_ClonesRecord(t *testing.T) {
	// A (TraceHandler) adds trace_id/span_id to its clone of the record.
	// B (plain handler) gets its own independent clone and should NOT see those attrs.
	ctx, _, _ := newTraceContext(t)

	var bufA, bufB bytes.Buffer
	fanout := otel.FanoutHandler{
		&otel.TraceHandler{Handler: slog.NewJSONHandler(&bufA, nil)}, // A: adds trace attrs
		slog.NewJSONHandler(&bufB, nil),                              // B: plain, no trace attrs
	}
	slog.New(fanout).InfoContext(ctx, "test")

	var gotB map[string]any
	if err := json.Unmarshal(bufB.Bytes(), &gotB); err != nil {
		t.Fatalf("parse B JSON: %v\noutput: %s", err, bufB.String())
	}
	if _, ok := gotB["trace_id"]; ok {
		t.Error("handler B received trace_id — FanoutHandler did not clone the record independently")
	}
}
