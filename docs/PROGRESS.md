# Progress log

One row per session. Numbers, not adjectives (CLAUDE.md rule 6).

| Date | Session | Shipped | Numbers | Artifact |
| --- | --- | --- | --- | --- |
| 28 Oct 2026 | Day 0 | WSL2, Go 1.27, Docker; public repo; gateway `/healthz` + graceful shutdown; first test | — | `labs/day-00.md`, commit `623902c` |
| 29 Oct 2026 | Day 1 | `LogRequests` middleware + own test; kind + ko; gateway on kind with probes; pod-kill recovery observed | control-plane 695 MiB | `labs/day-01.md`, commit `4be5819` |
| 29 Oct 2026 | Plan | Re-sized plan for 8 GB: lean profile + Codespaces for heavy work | WSL cap 5 GB / swap 4 GB | CLAUDE.md |
| 11 Nov 2026 | Prep | Read Postgres transactions, Read Committed, `ON CONFLICT`, pgx, CloudNativePG; Student Pack confirmed | — | notes |
| 10 Nov 2026 | Day 2/3 | CNPG Postgres (primary + replica); `001_init.sql`; `httpx` refactor; payments with idempotency + transactional outbox | cluster ~1.8 GiB of 4.8 GiB | `labs/day-02.md`, commits `ee41706`, `ae541e5`, `feaf917` |
| 17 Nov 2026 | Day 3.5 (debt) | CI billing lock confirmed cleared (green in 1m17s); `cluster.yaml` 3 nodes to 1; `scripts/up.sh` + `migrate.sh` + `smoke.sh`; Day 3 contract verified | bring-up 1 command / ~3 min; smoke 11/11 pass; cluster 2.2 GiB of 4.8 (was 2.5 on 3 nodes) | this file, `scripts/` |

## Open debt

- [ ] `docs/adr/002-transactional-outbox.md` is still an unfilled template — every section is a placeholder. Source material is already written in `labs/day-02.md` Q2.
- [ ] `docs/adr/001-local-platform-kind-ko.md` does not exist (the kind + ko decision was made on Day 1, never recorded).
- [x] ~~No `scripts/up.sh`~~ — done 17 Nov: `up.sh` (full bring-up, `--deploy` for a fast service-only redeploy), `migrate.sh` (tracks applied files in `schema_migrations`), `smoke.sh` (11 assertions on the idempotency contract).
- [ ] `README.md` is a 3-line stub.

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
