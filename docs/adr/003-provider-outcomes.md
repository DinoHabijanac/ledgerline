# ADR 003: Handling payment provider outcomes

Status: accepted 19 Nov 2026.

## Context

psp-sim is the first dependency in Ledgerline that sits outside a database
transaction. Everything before it — the payment row and its outbox row — is
written inside one Postgres transaction and can be rolled back. A charge made
at an external provider cannot be rolled back: the money has moved in someone
else's system.

The Two Generals' Problem says it is impossible to know, over an unreliable
network, whether a message arrived. So "did the charge happen?" is not always
answerable. The design has to make that explicit instead of guessing.

## Decision 1: the provider contract

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

In Go this is two sentinel errors, `errPSPUnavailable` and `errPSPUnknown`,
and the handler branches on `errors.Is(err, errPSPUnavailable)`. **Anything
that is not explicitly `errPSPUnavailable` is treated as unknown.** The
default is the safe one, so a new failure mode nobody anticipated lands in
"unknown" rather than being silently mistaken for "failed".

A detail worth naming: a `200` whose body we cannot decode is **unknown**, not
failed. The provider said it charged the card; our inability to read the
reference does not undo that.

## Decision 2: how payments reacts

Only for a newly created payment. Replays never reach the provider.

| Outcome | status | psp_ref | event | HTTP |
| --- | --- | --- | --- | --- |
| charged | `authorized` | stored | `payment.authorized` | 201 |
| `errPSPUnavailable` | `failed` | NULL | `payment.failed` | 201 |
| unknown | stays `pending` | NULL | **none** | 202 |

The status change and its event are written in one transaction, for the same
reason as [ADR-002](002-transactional-outbox.md): an authorized payment with no
`payment.authorized` event would be invisible to every downstream consumer,
including the ledger.

`UPDATE … WHERE id = $1 AND status = 'pending'` guards the write. If the row
has already left `pending`, the update matches nothing, no event is emitted and
we return success. That makes `SetOutcome` idempotent and safe to call twice,
which matters because reconciliation will eventually call it too.

**201 for a failed payment** is deliberate: the HTTP status describes the
*resource* (a payment was created) and the body describes the *money*
(`"status":"failed"`). 402 was considered and rejected — it would conflate a
provider decision with a protocol-level error, and clients must read the body
to distinguish authorized from failed in any case.

**202 for unknown** is the honest answer: "we have your request, the outcome is
not settled." Not 500, which would invite a retry with a *new* idempotency key
and risk a second charge. Not 201, which would claim more than we know.

## Decision 3: `context.WithoutCancel` for the whole charge

The charge *and* the outcome write both run on
`context.WithoutCancel(r.Context())`, which keeps the request's values (and
later its trace) while dropping its cancellation.

Once we have decided to charge a card, abandoning halfway because the client
hung up converts a *knowable* outcome into an unknown one — the most expensive
state in the system. The client leaving does not un-charge the card. The 2s
client timeout, not the caller's patience, is what bounds the work.

Alternative considered: pass `r.Context()` to the provider call so a client
disconnect aborts it. Rejected — it manufactures pending payments for no gain.
It would also lose the outcome of a charge that had *already succeeded* when the
client disconnected, which is the worst case available.

Note this does not make psp-sim's own cancellation handling pointless: when
payments' 2s `http.Client` timeout fires it closes the connection, psp-sim sees
its request context cancelled, and abandons the work.

## Decision 4: timeout budgets shrink with depth

```
gateway   3000ms   ─┐
payments  2000ms    │  http.Client timeout to psp-sim
psp-sim    ~75ms    │  configured latency + jitter
                    └─ each layer leaves room for the one inside it
```

The outer budget must **exceed** the inner one. If the gateway gave up at 2s or
less it would cut payments off mid-charge: the client would get a generic 504
while payments carried on (`WithoutCancel`), charged the card and recorded an
outcome the client never learned about.

With 3s, payments' own timeout fires first, so payments — the service that
knows the most — is the one that decides and answers. Measured end to end
through the gateway with a 3000ms provider: **2012ms** and a 202, confirming the
inner timeout fires first. The spare second covers two network hops and the
database writes.

## Consequences

- **A reconciliation job is now required and does not exist yet.** Payments can
  sit in `pending` indefinitely. `migrations/002_psp.sql` adds a partial index
  on `(created_at) WHERE status = 'pending'` for exactly that query.
- **Ordinary deploys manufacture unknown outcomes.** Discovered while building
  the smoke test: during a rolling update of psp-sim, kube-proxy briefly routes
  to the old pod's IP after that pod is gone. A SYN to a dead pod IP receives no
  RST, so the caller hangs until its own timeout. Pending payments are therefore
  a routine consequence of deployment, not an exotic edge case — which raises
  the priority of reconciliation considerably.
- Clients must handle 202 and must reuse the same idempotency key when
  retrying. A retry with a fresh key is a second payment.
- The ledger cannot assume every payment reaches a terminal state promptly, so
  the future ledger-correctness SLO has to account for legitimately pending
  rows.
- We send `Idempotency-Key` to the provider but do **not** retry yet. Sending
  the key is what earns the right to retry later; retrying without provider-side
  deduplication would be the double-charge bug this ADR exists to prevent.
