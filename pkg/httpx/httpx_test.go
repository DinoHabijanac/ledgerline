package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// 1. What does the function take? Build each input. That's ARRANGE.
// 2. What does it give back? Call it, then use the result. That's ACT.
// 3. What's its job? Check that the job got done. That's ASSERT.
// 4. Make the test fail on purpose once, to prove it can.
func TestLogRequests(t *testing.T) {
	//ARRANGE
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil)) //create JSONHandler that writes to &buf using given options (nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) //StatusTeapot = status code 418
	})

	handler := LogRequests(logger, next)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	//ACT
	handler.ServeHTTP(rec, req)

	//ASSERT
	if !strings.Contains(buf.String(), `"status":418`) {
		t.Fatalf("log does not contain status 418, got: %s", buf.String())
	}
}

// A request carrying a trace must log its trace_id, so a log line can be
// looked up in Jaeger. A request without one must not log an empty or zero ID.
func TestLogRequestsIncludesTraceID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := LogRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/payments", nil).WithContext(ctx))
	if !strings.Contains(buf.String(), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Errorf("log missing trace_id: %s", buf.String())
	}

	buf.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/payments", nil))
	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("untraced request logged a trace_id: %s", buf.String())
	}
}
