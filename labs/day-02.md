Q1: Two requests with the same key at the same instant
- idempotency_key is UNIQUE, so Postgres won't allow two rows with the same key.
- The second INSERT waits until the first transaction commits or rolls back.
- If the first commits: ON CONFLICT DO NOTHING means the second inserts nothing and returns no row (ErrNoRows). The code then SELECTs the existing row and compares the request hashes. Same hash returns 200 with the original payment; different hash returns 422.
- If the first rolls back: the second INSERT goes through normally.
- Either way you end up with exactly 1 payment and 1 outbox row, because the database enforces the rule, not the Go code.

Q2: Why payment and outbox share a transaction
- One transaction is all or nothing: either both rows exist or neither does.
- Publishing to NATS right after commit means two separate writes (the "dual write" problem). If the pod crashes between the commit and the publish, the payment exists but the event is lost forever. If you publish before commit, you can announce a payment that then rolls back.
- With an outbox, a separate worker later reads rows WHERE published_at IS NULL, publishes them and marks them published. Nothing gets lost, but an event can be sent twice, so consumers must ignore duplicates.

Q3: How many replicas fit
- In theory 50 / 5 = 10 replicas.
- But other things also use connections: Postgres keeps 3 reserved for superusers, the CloudNativePG operator and its monitoring use some, so do your own psql sessions and migrations, and future services (for example the outbox worker).
- During a rolling update, old and new pods run at the same time, so connections briefly double.
- Realistically about 6–8 replicas, keeping headroom.

Q4: /readyz checks the DB, /healthz doesn't
- Readiness answers "can I serve traffic right now?" If the DB is down, payments can't work, so Kubernetes stops sending traffic to the pod but doesn't kill it.
- Liveness answers "is the process alive?" If it checked the DB, a DB outage would make Kubernetes restart every payments pod in a loop. That doesn't fix the DB and adds connection storms.
- It's the same rule as in your day-01.md: liveness checks local health only.

Q5: docker stats, compared with Thursday
- Thursday (from your day-01.md): ledgerline-control-plane at 695 MiB.
- Now: control-plane 854 MiB, worker 575 MiB, worker2 407 MiB, so about 1.8 GiB in total out of 4.8 GiB.
- The increase comes from the CNPG operator, 2 Postgres instances and the payments pod.
- If your Thursday cluster also had worker nodes, use their numbers from Thursday instead. Your notes only recorded the control-plane.