// Package telemetry wires OpenTelemetry tracing into a service in one call.
//
// The picture to hold in your head:
//
//	gateway ──HTTP──► payments ──HTTP──► psp-sim
//	   │                 │ └──SQL──► Postgres
//	   ▼                 ▼               ▼
//	 spans             spans           spans      each service exports its own
//	   └────────────┬────┴───────────────┘        spans over OTLP...
//	                ▼
//	         otel-collector  ──►  Jaeger          ...and Jaeger stitches them into
//	                                              one trace by trace ID.
//
// Two separate jobs happen here, and it is worth keeping them apart:
//
//   - PROPAGATION: carrying the trace ID from one service to the next inside
//     the `traceparent` HTTP header. This is what makes three services' spans
//     into ONE trace rather than three unrelated ones.
//   - EXPORT: shipping finished spans to the collector. This is what makes
//     them visible.
//
// Propagation is always on. Export only happens when
// OTEL_EXPORTER_OTLP_ENDPOINT is set, so `go run` and the unit tests need no
// collector, and a service with no collector still passes trace context along.
package telemetry

import (
	"context"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup installs the global propagator and, if a collector is configured, a
// tracer provider that exports to it. The returned function flushes buffered
// spans and must be called on shutdown: spans are sent in batches, so without
// it the last second of spans before a pod stops is simply lost.
func Setup(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	// W3C Trace Context (`traceparent`) is the standard header every OTel
	// library and Jaeger understand. Baggage rides along for free.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	// The exporter reads OTEL_EXPORTER_OTLP_ENDPOINT itself, including the
	// http:// scheme, which tells it not to use TLS inside the cluster.
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	// service.name is how Jaeger labels every span from this process.
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		// Batching: spans are queued in memory and sent every few seconds, so
		// tracing adds no network round trip to any request.
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		// Keep everything in a lab. ParentBased means "if the caller already
		// decided to sample this trace, follow that decision", so a trace is
		// never half-recorded across services. Production would sample a
		// fraction at the edge (gateway) and let the rest follow.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Server wraps a handler so each incoming request becomes a server span. It
// reads `traceparent` from the request, so the span joins the caller's trace
// instead of starting a new one.
//
// Health and readiness probes are filtered out. Kubernetes calls them every
// few seconds, and tracing them would bury the four spans you actually care
// about under thousands of identical ones -- the same reason probes are not
// worth logging (day-01).
func Server(h http.Handler, operation string) http.Handler {
	return otelhttp.NewHandler(h, operation,
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

// Transport wraps an outgoing HTTP transport so each call becomes a client
// span AND carries `traceparent` to the next service. Forgetting this on one
// hop is the classic way to end up with two disconnected traces.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Host + r.URL.Path
		}),
	)
}
