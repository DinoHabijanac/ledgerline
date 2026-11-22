#!/usr/bin/env bash
# Gate 1 evidence: send ONE payment through the gateway and prove it produced
# ONE trace spanning gateway -> payments -> psp-sim -> Postgres.
#
# The trick: we pick the trace ID ourselves and send it in a W3C `traceparent`
# header, exactly as an upstream service would. curl becomes "service zero".
# The gateway's otelhttp handler extracts it and every span downstream joins
# that trace -- so we know which trace to fetch without searching for it.
#
#   traceparent: 00-<32 hex trace id>-<16 hex parent span id>-01
#                 │                                           └ sampled
#                 └ version
set -uo pipefail

NS=${NS:-ledgerline}
GW=18080
JG=16686

pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done; }
trap cleanup EXIT

kubectl -n "$NS" rollout status deploy/otel-collector deploy/jaeger --timeout=120s >/dev/null || exit 1

kubectl -n "$NS" port-forward svc/gateway "$GW:80" >/dev/null 2>&1 & pids+=($!)
kubectl -n "$NS" port-forward svc/jaeger "$JG:16686" >/dev/null 2>&1 & pids+=($!)
for _ in $(seq 1 60); do
  curl -fsS "localhost:$GW/healthz" >/dev/null 2>&1 && curl -fsS "localhost:$JG/" >/dev/null 2>&1 && break
  sleep 0.25
done

trace_id=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
parent_id=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')

echo "trace id: $trace_id"
code=$(curl -sS -o /tmp/trace.out -w '%{http_code}' -X POST "localhost:$GW/v1/payments" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: trace-$trace_id" \
  -H "traceparent: 00-$trace_id-$parent_id-01" \
  --data '{"account_id":"acct_trace","amount_cents":4200,"currency":"EUR"}')
echo "payment: HTTP $code $(cat /tmp/trace.out)"

# Spans are batched twice -- by the SDK in each service (~5s) and by the
# collector (2s) -- so the trace appears a few seconds after the request.
# Poll until all the expected services have reported, rather than sleeping.
for _ in $(seq 1 40); do
  curl -fsS "localhost:$JG/api/traces/$trace_id" -o /tmp/trace.json 2>/dev/null \
    && python3 - /tmp/trace.json <<'PY' && break
import json, sys
t = json.load(open(sys.argv[1]))["data"][0]
svcs = {p["serviceName"] for p in t["processes"].values()}
sys.exit(0 if {"gateway", "payments", "psp-sim"} <= svcs else 1)
PY
  sleep 1
done

python3 - /tmp/trace.json <<'PY'
import json, sys
try:
    t = json.load(open(sys.argv[1]))["data"][0]
except Exception as e:
    print(f"\nFAIL: trace not found in Jaeger ({e})"); sys.exit(1)

spans = {s["spanID"]: s for s in t["spans"]}
svc = lambda s: t["processes"][s["processID"]]["serviceName"]
children = {}
roots = []
for s in spans.values():
    parent = next((r["spanID"] for r in s.get("references", [])
                   if r["refType"] == "CHILD_OF" and r["spanID"] in spans), None)
    (children.setdefault(parent, []) if parent else roots).append(s)

def show(s, depth):
    tags = {x["key"]: x["value"] for x in s.get("tags", [])}
    extra = [f"{k}={tags[k]}" for k in ("payment.outcome", "psp.injected_delay_ms") if k in tags]
    print(f"{'  ' * depth}{svc(s):<9} {s['operationName']:<45} {s['duration']/1000:8.1f} ms  {' '.join(extra)}")
    for c in sorted(children.get(s["spanID"], []), key=lambda c: c["startTime"]):
        show(c, depth + 1)

print(f"\n{len(spans)} spans in one trace:\n")
for r in sorted(roots, key=lambda r: r["startTime"]):
    show(r, 0)

services = {svc(s) for s in spans.values()}
db = [s for s in spans.values()
      if any(x["key"].startswith("db.") for x in s.get("tags", []))]
checks = [
    ("gateway span",  "gateway" in services),
    ("payments span", "payments" in services),
    ("psp-sim span",  "psp-sim" in services),
    ("Postgres span", len(db) > 0),
    ("single root (nothing orphaned into a separate trace)", len(roots) == 1),
]
print()
for name, ok in checks:
    print(f"  {'PASS' if ok else 'FAIL'}  {name}")
sys.exit(0 if all(ok for _, ok in checks) else 1)
PY
rc=$?

echo
echo "Open it:  kubectl -n $NS port-forward svc/jaeger 16686:16686"
echo "          http://localhost:16686/trace/$trace_id"
exit $rc
