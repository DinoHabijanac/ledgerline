# Ledgerline

A small Go payments platform with a double-entry ledger, built to be broken (chaos game days) and audited (SBOMs, signing, VEX, mappings to EU DORA and CRA). The code, ADRs and `docs/PROGRESS.md` hold the details; this file only covers what they don't.

## Constraints

- €0 budget: open source, the GitHub free tier and the Codespaces quota only. Don't add paid services or hosts; the evidence in the repo is the demo.
- Dev machine: an 8 GB laptop with WSL2 capped at 5 GB. Docker Desktop has to be running before bring-up (`up.sh` checks).
- Lean profile on the laptop: single-node kind, 1 replica per service (gateway 2), NATS with 1 node, Jaeger in-memory. Chaos Mesh only from week 9 (28 Dec 2026), the Sigstore policy-controller from week 12 (18 Jan 2027), and Dependency-Track never on the laptop.
- Heavy work (game days, Dependency-Track, re-runs) goes to a 4-core / 16 GB Codespace. Delete it once the results are pushed.
- Close browser tabs before bring-up; check `docker stats --no-stream` when memory is in doubt.

## Every session starts from zero

The cluster is deleted at the end of each session. Bring-up takes about 3 minutes:

```bash
./scripts/up.sh            # cluster, CNPG, Postgres, migrations, services
./scripts/up.sh --deploy   # rebuild and redeploy the Go services only
kind delete cluster --name ledgerline   # end of session
```

Jaeger is in-memory: capture any trace evidence into `docs/evidence/` before teardown.

## When a change is done

- Go code: `go vet ./...` and `go test -race ./...` pass. A Stop hook runs the tests at the end of every turn while Go files differ from `origin/main`; Go's test cache keeps an unchanged re-run at ~0.4 s (single sample).
- Anything that touches the running platform: `./scripts/smoke.sh` and `./scripts/trace.sh` pass against a fresh bring-up. Paste their final lines into the session.
- A design decision gets an ADR in `docs/adr/` (next free number).
- Work goes on a branch and lands through a PR with green CI.

## Writing

- Numbers, not adjectives: latency, memory, time to detect, time to recover. Say when a number is a single sample.
- Regulation: cite EUR-Lex article numbers (DORA = Reg. (EU) 2022/2554, CRA = Reg. (EU) 2024/2847). Write "maps to" or "provides evidence for", not "compliant". Write "EU DORA" in full, because "DORA metrics" is an unrelated DevOps term.
- `labs/day-NN.md` files are questions for the author to answer. Don't write answers in them.
