#!/usr/bin/env bash
# Gate 1 evidence: send ONE payment through the gateway and prove it produced
# ONE trace spanning gateway -> payments -> psp-sim -> Postgres.
#
# The gateway starts the trace (its span is the true root) and hands the trace
# ID back in the X-Trace-Id response header, so we know which trace to fetch.
#
# An earlier version invented a `traceparent` header in curl instead. That made
# the gateway's span claim a parent span that curl never reported, and Jaeger
# rightly flagged the trace "Incomplete". Lesson: a traceparent says "my span
# exists and is your parent" -- only send one if that span will really be
# exported.
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

key="trace-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
code=$(curl -sS -D /tmp/trace.hdr -o /tmp/trace.out -w '%{http_code}' -X POST "localhost:$GW/v1/payments" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $key" \
  --data '{"account_id":"acct_trace","amount_cents":4200,"currency":"EUR"}')
trace_id=$(grep -i '^x-trace-id:' /tmp/trace.hdr | awk '{print $2}' | tr -d '\r')
echo "payment: HTTP $code $(cat /tmp/trace.out)"
[[ -n "$trace_id" ]] || { echo "FAIL: gateway returned no X-Trace-Id header"; exit 1; }
echo "trace id: $trace_id"

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
roots = []      # spans with NO parent reference at all: true roots
dangling = []   # spans pointing at a parent that is not in the trace
for s in spans.values():
    refs = [r["spanID"] for r in s.get("references", []) if r["refType"] == "CHILD_OF"]
    if not refs:
        roots.append(s)
    elif refs[0] in spans:
        children.setdefault(refs[0], []).append(s)
    else:
        dangling.append(s)
        roots.append(s)   # still print it, at the top

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
    ("exactly one root span", len(roots) == 1),
    # What Jaeger means by "Incomplete": some span names a parent that never
    # arrived. An earlier version of this script missed it by counting such a
    # span as a root.
    ("complete: no span references a missing parent", not dangling),
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
