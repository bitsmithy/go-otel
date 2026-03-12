package otel

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Trace creates a span, calls fn with the span's context and the span itself,
// and ends the span when fn returns. If fn returns an error, the span records
// the error and sets its status to Error.
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
