---
name: end-session
description: Close a Ledgerline build session - evidence, verification, lab questions, PROGRESS row, PR, teardown. Use when the user says they're done, wrapping up, stopping for today, or asks to end the session.
---

# End a build session

Work through these in order. Each step says what "done" means. If the user is short on time, do steps 1, 2 and 6 and list what was skipped as open debt.

## 1. Evidence before teardown

Jaeger is in-memory and the cluster is about to be deleted. If this session changed anything observable on the platform, capture it now into `docs/evidence/` (trace output, timings, screenshots the user took). Name files after the session or gate, e.g. `day-06-outbox-relay.txt`.

Done: every number that will go into the PROGRESS row has a file or a command output behind it.

## 2. Verify

- Go changed: `go vet ./...` and `go test -race ./...` pass.
- Platform changed: `./scripts/smoke.sh` and `./scripts/trace.sh` pass on the running cluster. Paste their final lines.

If something fails, fix it or record it as open debt. Never report it as passing.

## 3. ADR, if we made a design decision

A choice between alternatives that someone will later ask "why?" about gets an ADR in `docs/adr/` (next free number, same structure as the existing ones). Don't write ADRs the user marked as theirs to write; check Open debt in `docs/PROGRESS.md`.

## 4. Lab questions

Write `labs/day-NN.md` (NN = this session's number in the PROGRESS log) with 3–5 questions about today's code:

- At least one per SRE question that applies: what breaks if this is deleted, what happens when it's slow or never answers, what happens if it runs twice or a thousand times at once.
- Point each question at a specific file, function or line.
- Questions only. No answers, and no note saying Claude wrote them.

## 5. PROGRESS row

Add a row to the session table in `docs/PROGRESS.md`: date, session name, hours, what shipped, numbers, artifacts.

- **Hours: ask the user. Never estimate them.**
- Numbers, not adjectives. Mark single samples as such.
- Update Open debt: tick what was closed, add what was found.
- Update Verified contracts or Gates if they changed.

## 6. Commit, PR, CI

- Commit on the session's branch with a message that says what and why.
- Ask the user before pushing. Then push and open or update the PR.
- The PR description starts with its review tier (see Review tiers in `CLAUDE.local.md`). A PR that only touches `labs/`, `docs/PROGRESS.md`, `docs/evidence/` or `README.md` is tier 1 and merges itself.
- Wait for CI (`test`, `lint`, `vulncheck`, `e2e`) and report the result. Don't merge; that's the user's call.

## 7. topics-covered

Update the user's `topics-covered.txt` (path in `CLAUDE.local.md`): what was learned or practised today, and any lab questions still open.

## 8. Teardown

Only after steps 1–2, and after confirming with the user that nothing else needs the cluster:

```bash
kind delete cluster --name ledgerline
```

## 9. Hand-off

Finish with five lines at most:
- what shipped, with the PR link and CI status
- what's still open
- the one concept tonight's theory session should start from (input for `/theory-session`)
