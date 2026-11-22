# Progress log

One row per session. Numbers, not adjectives (CLAUDE.md rule 6).

| Date | Session | Shipped | Numbers | Artifact |
| --- | --- | --- | --- | --- |
| 28 Oct 2026 | Day 0 | WSL2, Go 1.27, Docker; public repo; gateway `/healthz` + graceful shutdown; first test | — | `labs/day-00.md`, commit `ebc3d55` |
| 29 Oct 2026 | Day 1 | `LogRequests` middleware + own test; kind + ko; gateway on kind with probes; pod-kill recovery observed | control-plane 695 MiB | `labs/day-01.md`, commit `bb24c1e` |
| 29 Oct 2026 | Plan | Re-sized plan for 8 GB: lean profile + Codespaces for heavy work | WSL cap 5 GB / swap 4 GB | CLAUDE.md |
| 10 Nov 2026 | Day 2/3 | CNPG Postgres (primary + replica); `001_init.sql`; `httpx` refactor; payments with idempotency + transactional outbox | cluster ~1.8 GiB of 4.8 GiB | `labs/day-02.md`, commit `26ba052` |
| 11 Nov 2026 | Prep | Read Postgres transactions, Read Committed, `ON CONFLICT`, pgx, CloudNativePG; Student Pack confirmed | — | notes |
| 17 Nov 2026 | Day 3.5 (debt) | CI billing lock confirmed cleared (green in 1m17s); `cluster.yaml` 3 nodes to 1; `scripts/up.sh` + `migrate.sh` + `smoke.sh`; Day 3 contract verified | bring-up 1 command / ~3 min; smoke 11/11 pass; cluster 2.2 GiB of 4.8 (was 2.5 on 3 nodes) | this file, `scripts/` |
| 18 Nov 2026 | Day 4 Part A | `services/psp-sim`: fake provider with configurable latency, jitter and error rate; cancellation-aware handler; 200/503/no-answer contract documented in ADR-003 | 10 test funcs green under `-race` in 2.0 s | `labs/day-04.md`, `docs/adr/003` |
| 19 Nov 2026 | Day 4 Part A (A2) + Part B | `migrations/002_psp.sql`; psp-sim deployed; PSP client with a 2 s budget; `SetOutcome`; the three-outcome switch with `context.WithoutCancel`; gateway reverse proxy with a 3 s budget; ADR-003 | smoke 27/27; slow-provider round trip 2012 ms (inner 2 s fired, not the outer 3 s); 29 Go tests green under `-race` | `docs/adr/003`, `labs/day-04.md` |
| 22 Nov 2026 | Day 5 — **Gate 1 met** | OpenTelemetry in all three services (`pkg/telemetry`); otelpgx DB spans; `trace_id` on every log line; OTel Collector 0.165.0 + Jaeger 2.23.0 in-cluster; `scripts/trace.sh`; ADR-004 | one trace, 15 spans, single root, gateway → payments → psp-sim → Postgres; 72.1 ms end to end, psp-sim 79 %, Postgres 11 %; cold vs warm `INSERT` 6.9 vs 1.2 ms; smoke still 27/27 | `docs/evidence/gate1-trace.txt`, `docs/adr/004`, `labs/day-05.md` |

## Open debt

- [ ] `docs/adr/002-transactional-outbox.md` is still an unfilled template — every section is a placeholder. Source material is already written in `labs/day-02.md` Q2.
- [ ] `docs/adr/001-local-platform-kind-ko.md` is a skeleton with placeholders (kind + ko decision from Day 1).
- [x] ~~No `scripts/up.sh`~~ — done 17 Nov: `up.sh`, `migrate.sh`, `smoke.sh`.
- [ ] `README.md` is a 3-line stub. Worth fixing now that there is something to show: a G1 trace and three ADRs.
- [ ] Reconciliation job for `pending` payments (ADR-003 consequence; ordinary deploys create them).

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
