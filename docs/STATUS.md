# Status

What the platform is verified to do, and where it stands against its gates. Each claim points to the script or evidence that reproduces it.

## Verified contracts

Re-runnable with `./scripts/smoke.sh`, which drives the real cluster through
the gateway. Last green 22 Nov 2026, **27/27**, with tracing on.

| Phase | Assertion | Result |
| --- | --- | --- |
| A | healthy provider | 201 `authorized`, `psp_ref` stored, `payment.created` + `payment.authorized` |
| B | replay, identical body | 200, same id, same ref, **no duplicate events** |
| B | replay, same key, different body | 422 |
| C | streaming replication | row readable through `pg-ro` |
| D | provider returns 503 | 201 `failed`, `psp_ref` NULL, `payment.failed` |
| E | provider slower than the 2 s budget | 202, stays `pending`, **no terminal event**, ~2.0 s |
| F | replay of an unsettled payment | 200, still `pending`, provider not called again |

Phase E is the one that matters: a timeout leaves the payment `pending` and
publishes nothing, because the charge may have succeeded. See
[ADR-003](adr/003-provider-outcomes.md).

Tracing is verified separately by `./scripts/trace.sh`: it sends a payment with
a chosen `traceparent` and asserts Jaeger holds one trace with gateway,
payments, psp-sim and Postgres spans under a single root.

## Gates

| Gate | Date | Requirement | State |
| --- | --- | --- | --- |
| G1 | 29 Nov 2026 | 3 Go services on kind, one trace end to end | **met 22 Nov 2026**, 7 days early — `docs/evidence/gate1-trace.txt` |
| G2 | 3 Jan 2027 | SLOs, dashboards, chaos tooling live | not started |
| G3 | 17 Jan 2027 | Game Day 1 postmortem published, re-run proves the fix | not started |
| G4 | 7 Feb 2027 | Signed releases with SBOMs, CRA drill report | not started |
| G5 | 28 Mar 2027 | 2 merged upstream PRs, 2 Evidence Briefs | not started |
