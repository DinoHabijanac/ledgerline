# ADR 005: What the Claude Code hooks block on

Status: accepted 25 Nov 2026.

## Context

Claude Code writes much of the code in this repo (see `CLAUDE.md`). Two hooks in
`.claude/settings.json` give it feedback without the author having to ask:

- PostToolUse on every Edit/Write: `gofmt -l` on the file, `go vet` on its package.
- Stop, at the end of every turn: `go test -race ./...` when Go files differ from
  `origin/main`. It gives up after 3 failed attempts so it can't loop forever.

A hook that exits 2 hands its stderr back to Claude as an error to fix now. The
edit has already happened; exit 2 can't undo it, it only interrupts.

The first version blocked on both gofmt and vet. A `/code-review` on PR #1 showed
the cost: in a multi-file change (rename `SetOutcome` in `store.go`, then
`handler.go`), the package fails vet between the two edits. Claude was told to
fix an error its next planned edit would fix anyway. The noise costs attention,
and in the bad case leads to the wrong fix (a compatibility shim instead of
finishing the rename).

## Decision

- **gofmt blocks (exit 2).** A formatting error is always local to the file just
  written and is never an expected intermediate state.
- **go vet informs (exit 0 with `additionalContext`).** Claude still sees vet's
  output right after the edit, worded as a note, not a demand.
- **The hard gates are the Stop hook's `go test -race ./...` and CI** (`go vet`
  and `go test -race` on every PR). `go test` also runs a subset of vet's checks
  (printf, atomic, …) on its own.

Options considered: block on both (fastest feedback, noisy on multi-file work,
which the ledger service will be), and drop vet from the hook (simplest, but vet
findings arrive only at the end of the turn or in CI).

## Consequences

- A real vet finding can survive until the end of the turn. It can't reach `main`:
  CI runs full `go vet`.
- Cost of the Stop hook when nothing changed: 0.42 s (single sample; Go's test
  cache). Cold cache: 42.7 s, 334 MB (single sample).
- Open: the Stop hook's "Go changed?" check reads a local `origin/main` that can
  be stale. False positives are the safe side and cost ~0.4 s, so it stays for
  now; tracked in `docs/PROGRESS.md` open debt.
