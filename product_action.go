package otel

import (
	"context"
	"fmt"
	"regexp"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	productActionCountMetric = "app.user.action.count"
	productActionItemsMetric = "app.user.action.affected_items"
	productActionEvent       = "app.user.action"
	ActorAnonymous           = "anonymous"
	ActorCook                = "cook"
	ActorGuest               = "guest"
	OutcomeSuccess           = "success"
	OutcomeRejected          = "rejected"
	OutcomeError             = "error"
)

var productActionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ProductAction describes one deliberate user intent.
type ProductAction struct {
	Name          string
	Actor         string
	Outcome       string
	Changed       *bool
	AffectedItems int64
	ErrorType     string
}

// ProductActionRecorder records the shared Product Action metrics and span event.
type ProductActionRecorder struct {
	count         metric.Int64Counter
	affectedItems metric.Int64Histogram
}

// NewProductActionRecorder creates a recorder from the provided meter.
func NewProductActionRecorder(meter metric.Meter) (*ProductActionRecorder, error) {
	count, err := meter.Int64Counter(
		productActionCountMetric,
		metric.WithDescription("Product Actions"),
		metric.WithUnit("{action}"),
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", productActionCountMetric, err)
	}
	affectedItems, err := meter.Int64Histogram(
		productActionItemsMetric,
		metric.WithDescription("Items affected by one Product Action"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", productActionItemsMetric, err)
	}
	return &ProductActionRecorder{count: count, affectedItems: affectedItems}, nil
}

// Record emits one Product Action count, an optional affected-item observation,
// and an event on the current recording span.
func (recorder *ProductActionRecorder) Record(ctx context.Context, action ProductAction) error {
	if err := validateProductAction(action); err != nil {
		return err
	}
	attrs := productActionAttributes(action)
	recorder.count.Add(ctx, 1, metric.WithAttributes(attrs...))
	if action.AffectedItems > 0 {
		recorder.affectedItems.Record(ctx, action.AffectedItems, metric.WithAttributes(attrs...))
	}
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.AddEvent(productActionEvent, trace.WithAttributes(attrs...))
	}
	return nil
}

func validateProductAction(action ProductAction) error {
	if !productActionNamePattern.MatchString(action.Name) {
		return fmt.Errorf("invalid Product Action name: %s", action.Name)
	}
	if action.Actor != ActorAnonymous && action.Actor != ActorCook && action.Actor != ActorGuest {
		return fmt.Errorf("invalid Product Action actor: %s", action.Actor)
	}
	if action.Outcome != OutcomeSuccess && action.Outcome != OutcomeRejected && action.Outcome != OutcomeError {
		return fmt.Errorf("invalid Product Action outcome: %s", action.Outcome)
	}
	if action.AffectedItems < 0 {
		return fmt.Errorf("product action affected items must not be negative")
	}
	return nil
}

func productActionAttributes(action ProductAction) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("app.user.action.name", action.Name),
		attribute.String("app.user.type", action.Actor),
		attribute.String("app.user.action.outcome", action.Outcome),
	}
	if action.Changed != nil {
		attrs = append(attrs, attribute.Bool("app.user.action.changed", *action.Changed))
	}
	if action.ErrorType != "" {
		attrs = append(attrs, attribute.String("error.type", action.ErrorType))
	}
	return attrs
}
