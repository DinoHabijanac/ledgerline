# ADR 002: Transactional outbox for payment events

## Context
<payments must notify other services that a payment was created; the DB write and the message must never disagree>

## Decision
<write the payment row and an outbox row in the same Postgres transaction; a separate relay publishes unpublished outbox rows later and sets published_at>

## Alternatives
<publish to NATS right after commit: lost events on crash (dual write); publish before commit: events for payments that never existed; distributed transactions (2PC): complex, NATS doesn't support it>

## Consequences
<+ no lost events, payment and event always consistent; − events are delivered at least once, so consumers must deduplicate; − a relay worker is needed (not built yet); − outbox table grows and needs cleanup>
Replace each <...> with one or two sentences of your own.