# ADR 004: Distributed tracing with OpenTelemetry, a Collector and Jaeger

Status: accepted 22 Nov 2026. Completes Gate 1.

## Context

One payment now crosses three services and a database. Logs show what each
service did, but not how the pieces relate in time, and not where the latency
of a single request went. When a payment is slow or ends up `pending`, the
question is "which hop?", and only a trace answers it.

Constraints: EUR 0, 8 GB laptop (lean profile), vendor-neutral so the backend
can change later, and nothing in the payment path may depend on tracing working.

## Decision

**Instrumentation: OpenTelemetry Go SDK, shared in `pkg/telemetry`.**

- Server spans via `otelhttp.NewHandler`, client spans via `otelhttp.NewTransport`
  on every outgoing HTTP client (gateway's reverse proxy, payments' PSP client).
- Database spans via `otelpgx` as the pgx `QueryTracer`.
- W3C Trace Context (`traceparent`) for propagation.
- Business attributes on spans: `payment.id`, `payment.outcome`
  (authorized / failed / unknown), `psp.injected_delay_ms`,
  `psp.injected_failure`, and a span event when psp-sim's caller gives up.
- `trace_id` stamped on every request log line, to go from a log to its trace.

**Pipeline: services -> OTel Collector -> Jaeger v2 (in-memory).**

Services only know "send OTLP to the collector". Swapping the backend, or adding
a second one, is a collector config change with no service rebuilt. Batching,
retries and the memory limit live in one place.

**Sampling: keep everything** (`ParentBased(AlwaysSample)`). Volume in a lab is
tiny. ParentBased means a downstream service follows the caller's decision, so a
trace is never half-recorded across services.

**What is deliberately not traced or recorded:**

- `/healthz` and `/readyz` — filtered out, otherwise probe spans every few
  seconds bury real traffic. otelpgx only creates DB spans under a recording
  parent, so the readiness ping to Postgres is invisible too (checked in the
  library source, `TraceQueryStart`).
- SQL query parameters — off (the library default). `db.query.text` carries the
  statement with `$1..$n` placeholders only, so account IDs and amounts never
  leave the database inside a trace. Verified in the captured trace.

**Tracing must never break payments.** If `telemetry.Setup` fails, the service
logs it and runs untraced. If `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, context is
still propagated but nothing is exported. Spans are exported in batches off the
request path.

## Alternatives

- **Services export straight to Jaeger.** One fewer pod (~64 Mi), but every
  backend change means touching every service, and there's no single place for
  memory limits or later for metrics. Rejected.
- **Jaeger with persistent storage, or Tempo.** Needs a database or object
  store; too heavy for the lean profile. In-memory is enough because the cluster
  is deleted every session. Evidence is captured to `docs/evidence/` instead.
- **Sampling a fraction.** Correct for production, pointless at lab volume, and
  it would make the evidence run non-deterministic.

## Consequences

- One request produces one trace across gateway, payments, psp-sim and Postgres.
  Evidence: `docs/evidence/gate1-trace.txt` (15 spans, single root).
- Measured on a warm connection: 72.1 ms end to end, of which psp-sim 56.8 ms
  (79 %) and Postgres ~8 ms (11 %).
- First found by tracing: pgx prepares each statement the first time it runs on
  a connection, an extra round trip visible as a child span. Cold vs warm:
  payment `INSERT` 6.9 ms vs 1.2 ms; create transaction 13.5 ms vs 3.9 ms.
- Cluster cost: collector (64 Mi request / 128 Mi limit) plus Jaeger (96 Mi / 256 Mi).
- `context.WithoutCancel` in the payments handler keeps the trace parent: it
  drops cancellation but keeps the context's values, and the span lives in a
  value. The PSP call and the outcome write stay in the request's trace.
- Traces are lost when Jaeger restarts. Anything worth keeping must be captured
  with `scripts/trace.sh` or a screenshot before `kind delete cluster`.
