# Status

What the platform is verified to do, and where it stands against its gates. Each claim points to the script or evidence that reproduces it.

## Verified contracts

Re-runnable with `./scripts/smoke.sh`. Last green 17 Nov 2026, 11/11.

| # | Assertion | Result |
| --- | --- | --- |
| 1 | `POST /v1/payments`, fresh key | 201, `status=pending` |
| 2 | replay, identical body | 200, same id |
| 3 | replay, same key, different body | 422 |
| 4 | rows written | 1 payment, 1 unpublished `payment.created` outbox row |
| 5 | streaming replication | row readable through `pg-ro` |

## Gates

| Gate | Date | Requirement | State |
| --- | --- | --- | --- |
| G1 | 29 Nov 2026 | 3 Go services on kind, one trace end to end | 2/3 services, no tracing |
| G2 | 3 Jan 2027 | SLOs, dashboards, chaos tooling live | not started |
| G3 | 17 Jan 2027 | Game Day 1 postmortem published, re-run proves the fix | not started |
| G4 | 7 Feb 2027 | Signed releases with SBOMs, CRA drill report | not started |
| G5 | 28 Mar 2027 | 2 merged upstream PRs, 2 Evidence Briefs | not started |
