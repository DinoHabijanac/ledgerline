package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installRecorder swaps the global tracer provider for one that keeps finished
// spans in memory, so a test can inspect exactly what would have been sent to
// Jaeger. WithSyncer exports each span the moment it ends -- no batching delay.
func installRecorder(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))

	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return exp
}

// The whole point of distributed tracing in one test: a client span in
// service A and a server span in service B must share a trace ID, and B's
// span must name A's span as its parent. If propagation is broken anywhere,
// they get different trace IDs and Jaeger shows two unrelated traces.
func TestClientAndServerSpansJoinOneTrace(t *testing.T) {
	exp := installRecorder(t)

	var gotTraceparent string
	backend := httptest.NewServer(Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}), "backend"))
	defer backend.Close()

	client := &http.Client{Transport: Transport(nil)}
	resp, err := client.Get(backend.URL + "/v1/charge")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// 1. The header actually went over the wire.
	if gotTraceparent == "" {
		t.Fatal("backend received no traceparent header: Transport is not propagating")
	}

	// 2. Two spans were recorded: one client, one server.
	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (client + server)", len(spans))
	}
	var clientSpan, serverSpan tracetest.SpanStub
	for _, s := range spans {
		switch s.SpanKind.String() {
		case "client":
			clientSpan = s
		case "server":
			serverSpan = s
		}
	}

	// 3. Same trace, and the server span hangs off the client span.
	if clientSpan.SpanContext.TraceID() != serverSpan.SpanContext.TraceID() {
		t.Errorf("client trace %s != server trace %s: spans would show as two separate traces",
			clientSpan.SpanContext.TraceID(), serverSpan.SpanContext.TraceID())
	}
	if serverSpan.Parent.SpanID() != clientSpan.SpanContext.SpanID() {
		t.Errorf("server span's parent is %s, want the client span %s",
			serverSpan.Parent.SpanID(), clientSpan.SpanContext.SpanID())
	}
}

// Probes run every few seconds forever. If they produced spans, Jaeger would
// be thousands of /healthz traces deep before your one payment appeared.
func TestProbesAreNotTraced(t *testing.T) {
	exp := installRecorder(t)

	h := Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), "svc")
	for _, path := range []string{"/healthz", "/readyz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if n := len(exp.GetSpans()); n != 0 {
		t.Errorf("probes produced %d spans, want 0", n)
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/payments", nil))
	if n := len(exp.GetSpans()); n != 1 {
		t.Errorf("a real request produced %d spans, want 1", n)
	}
}

// With no collector configured, Setup must not fail and must not try to
// export. This is what keeps `go run` and CI working without infrastructure.
func TestSetupWithoutCollectorIsANoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// The edge must hand the trace ID back to the caller, and it must be the ID of
// the trace that was actually recorded -- otherwise looking it up finds nothing.
func TestTraceIDHeaderMatchesRecordedTrace(t *testing.T) {
	exp := installRecorder(t)

	h := Server(TraceIDHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})), "edge")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/payments", nil))

	got := rec.Header().Get("X-Trace-Id")
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if want := spans[0].SpanContext.TraceID().String(); got != want {
		t.Errorf("X-Trace-Id = %q, want the recorded trace %q", got, want)
	}
	// No incoming traceparent, so the edge span must be a TRUE root. This is
	// what Jaeger checks when it decides whether a trace is complete.
	if spans[0].Parent.IsValid() {
		t.Errorf("edge span has parent %s, want none", spans[0].Parent.SpanID())
	}
}
