package otel_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	otel "github.com/bitsmithy/go-otel"
	"github.com/bitsmithy/go-otel/oteltest"
)

func TestProductActionRecorderRecordsMetricAndSpanEvent(t *testing.T) {
	harness := oteltest.Setup(t)
	recorder, err := otel.NewProductActionRecorder(harness.Meter)
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := harness.Tracer.Start(context.Background(), "request")
	changed := true
	if err := recorder.Record(ctx, otel.ProductAction{
		Name: "recipe.import.request", Actor: otel.ActorCook, Outcome: otel.OutcomeSuccess,
		Changed: &changed, Variant: "starter",
	}); err != nil {
		t.Fatal(err)
	}
	span.End()

	metric := oteltest.FindMetric(harness.Metrics(t), "app.user.action.count")
	sum := metric.Data.(metricdata.Sum[int64])
	if got, want := []any{sum.DataPoints[0].Value, harness.Spans()[0].Events[0].Name}, []any{int64(1), "app.user.action"}; !equalValues(got, want) {
		t.Fatalf("Product Action signals = %v, want %v", got, want)
	}
	wantAttrs := map[string]string{
		"app.user.action.name":    "recipe.import.request",
		"app.user.type":           "cook",
		"app.user.action.outcome": "success",
		"app.user.action.changed": "true",
		"app.user.action.variant": "starter",
	}
	if got := oteltest.MetricAttrs(sum.DataPoints[0].Attributes); !equalStringMaps(got, wantAttrs) {
		t.Fatalf("Product Action attributes = %v, want %v", got, wantAttrs)
	}
}

func TestProductActionRecorderRecordsOneBulkGestureAndAffectedItems(t *testing.T) {
	harness := oteltest.Setup(t)
	recorder, err := otel.NewProductActionRecorder(harness.Meter)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(context.Background(), otel.ProductAction{
		Name: "shopping.purchase.mark", Actor: otel.ActorCook, Outcome: otel.OutcomeSuccess, AffectedItems: 10,
	}); err != nil {
		t.Fatal(err)
	}
	rm := harness.Metrics(t)
	count := oteltest.FindMetric(rm, "app.user.action.count").Data.(metricdata.Sum[int64])
	items := oteltest.FindMetric(rm, "app.user.action.affected_items").Data.(metricdata.Histogram[int64])

	if got, want := []int64{count.DataPoints[0].Value, items.DataPoints[0].Sum}, []int64{1, 10}; !equalInt64s(got, want) {
		t.Fatalf("Product Action values = %v, want %v", got, want)
	}
}

func TestProductActionRecorderRejectsUnknownBoundedValues(t *testing.T) {
	harness := oteltest.Setup(t)
	recorder, err := otel.NewProductActionRecorder(harness.Meter)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []otel.ProductAction{
		{Name: "recipe.create", Actor: "operator", Outcome: otel.OutcomeSuccess},
		{Name: "recipe.create", Actor: otel.ActorCook, Outcome: "timeout"},
		{Name: "recipe.create", Actor: otel.ActorCook, Outcome: otel.OutcomeSuccess, Variant: "private choice"},
	} {
		if err := recorder.Record(context.Background(), action); err == nil {
			t.Fatalf("Record(%+v) succeeded, want validation error", action)
		}
	}
}

func equalInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func equalValues(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
