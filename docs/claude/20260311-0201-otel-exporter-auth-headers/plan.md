# Plan: OTLP Exporter Auth Headers

## Overview

Add a `Headers map[string]string` field to `Config` that passes programmatic headers to all three OTLP HTTP exporters (traces, metrics, logs) via `WithHeaders()`. When empty, the SDK's automatic `OTEL_EXPORTER_OTLP_HEADERS` env var reading remains the fallback.

## Tasks

### 1. Add `Headers` field to `Config` struct

**File**: `otel.go:31-54`

Add after the `Endpoint` field:

```go
// Headers is a set of key-value pairs added to every OTLP export request
// as HTTP headers. Use this for authentication tokens, API keys, etc.
// When empty the standard OTel environment variables are used as a fallback
// (OTEL_EXPORTER_OTLP_HEADERS, OTEL_EXPORTER_OTLP_TRACES_HEADERS, etc.).
Headers map[string]string
```

### 2. Pass headers to all three exporters

**File**: `otel.go:113-160`

For each exporter (traces, metrics, logs), append `WithHeaders(cfg.Headers)` to the options slice when `len(cfg.Headers) > 0`. Follow the existing `Endpoint` pattern:

```go
// Traces
var traceOpts []otlptracehttp.Option
if cfg.Endpoint != "" {
    traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
}
if len(cfg.Headers) > 0 {
    traceOpts = append(traceOpts, otlptracehttp.WithHeaders(cfg.Headers))
}
```

Same pattern for metrics (`otlpmetrichttp.WithHeaders`) and logs (`otlploghttp.WithHeaders`).

### 3. Update `Setup` doc comment

**File**: `otel.go:56-67`

The doc comment already mentions `OTEL_EXPORTER_OTLP_HEADERS`. No change needed — it remains accurate since the env var is still the fallback.

### 4. Add test for headers config (smoke test)

**File**: `otel_test.go`

Add a test that mirrors `TestSetup_EndpointOverride` — verifies that `Setup` succeeds when `Headers` is set (OTLP exporters connect lazily, so no actual network call):

```go
func TestSetup_HeadersOverride(t *testing.T) {
    shutdown, _, _, _, err := otel.Setup(context.Background(), otel.Config{
        ServiceName: "test-svc",
        Headers:     map[string]string{"Authorization": "Bearer test-token"},
    })
    if err != nil {
        t.Fatalf("Setup with Headers override returned error: %v", err)
    }
    _ = shutdown(cancelledCtx())
}
```

### 5. Add integration test: headers are actually sent

**File**: `otel_test.go`

Spin up an `httptest.Server` that records incoming request headers. Point `Setup` at it with an `Authorization` header, create a span, force-flush via `shutdown()`, and assert the server received the header.

The test server needs to accept POST requests on `/v1/traces` (the OTLP HTTP traces path) and return 200 with an empty protobuf response. We only need to verify the trace exporter sends headers — all three exporters use the same `WithHeaders()` plumbing, so testing one is sufficient.

```go
func TestSetup_HeadersSentToCollector(t *testing.T) {
    var receivedAuth atomic.Value

    collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if auth := r.Header.Get("Authorization"); auth != "" {
            receivedAuth.Store(auth)
        }
        w.WriteHeader(http.StatusOK)
    }))
    defer collector.Close()

    ctx := context.Background()
    shutdown, _, tracer, _, err := otel.Setup(ctx, otel.Config{
        ServiceName: "test-svc",
        Endpoint:    collector.URL,
        Headers:     map[string]string{"Authorization": "Bearer test-token"},
    })
    if err != nil {
        t.Fatalf("Setup: %v", err)
    }

    // Create a span to generate trace data for export.
    _, span := tracer.Start(ctx, "test-op")
    span.End()

    // shutdown flushes all pending exports to the collector.
    if err := shutdown(ctx); err != nil {
        t.Fatalf("shutdown: %v", err)
    }

    got, _ := receivedAuth.Load().(string)
    if got != "Bearer test-token" {
        t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
    }
}
```

Key design choices:
- Uses `atomic.Value` because the HTTP handler runs on a different goroutine.
- Only tests the trace exporter — all three use the same `WithHeaders()` mechanism, so one is sufficient to prove headers flow through.
- `shutdown(ctx)` with a live (non-cancelled) context forces a flush, ensuring the batch exporter sends pending spans before the test asserts.
- The test server returns 200 to any path, so it handles `/v1/traces` without needing path routing.

## Task Checklist

- [ ] Add `Headers map[string]string` to `Config` struct
- [ ] Pass `WithHeaders(cfg.Headers)` to trace exporter when headers are set
- [ ] Pass `WithHeaders(cfg.Headers)` to metric exporter when headers are set
- [ ] Pass `WithHeaders(cfg.Headers)` to log exporter when headers are set
- [ ] Add `TestSetup_HeadersOverride` smoke test
- [ ] Add `TestSetup_HeadersSentToCollector` integration test
- [ ] Run tests, verify all pass
