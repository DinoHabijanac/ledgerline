# ADR 003: Handling payment provider outcomes

Status: **in progress.** Part A (the provider contract) decided 17 Nov 2026.
Part B (how payments reacts to each outcome) still to be written.

## Context

psp-sim is the first dependency in Ledgerline that sits outside a database
transaction. Everything before it — the payment row and its outbox row — is
written inside one Postgres transaction and can be rolled back. A charge made
at an external provider cannot be rolled back: the money has moved in someone
else's system.

The Two Generals' Problem says it is impossible to know, over an unreliable
network, whether a message arrived. So "did the charge happen?" is not always
answerable. The design has to make that explicit instead of guessing.

## Decision, part A: the provider contract

psp-sim promises exactly three outcomes, and callers may rely on them:

| Response | Meaning | Caller may conclude |
| --- | --- | --- |
| `200` + `psp_ref` | the card **was** charged | authorized |
| `503` | the card was **not** charged | failed, safely |
| no answer (timeout, connection error) | **unknown** | nothing |

The 503 is the load-bearing part. It is a promise, not just an error code: it
is what allows payments to mark a payment `failed` without risking a double
charge during later reconciliation. An outcome that carries no promise — a
timeout — must not be collapsed into the same state.

## Decision, part B: how payments reacts

<To be written in the next session. Covers: newly-created payments only;
success -> authorized + psp_ref + payment.authorized event; errPSPUnavailable
-> failed + payment.failed event; timeout or network error -> leave pending and
return 202. Why the outcome is written with context.WithoutCancel. Why the
gateway's 3 s budget must exceed the client's 2 s timeout.>

## Alternatives

<To be written: retry on timeout (why not, without idempotency at the provider);
treat timeout as failure (double-charge risk); treat timeout as success
(phantom charges); synchronous reconciliation before answering the client.>

## Consequences

<To be written: pending payments need a reconciliation job that does not exist
yet; clients must handle 202; the ledger cannot assume every payment reaches a
terminal state promptly.>
