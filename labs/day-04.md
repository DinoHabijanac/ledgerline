# Day 4, Part A — the third service (psp-sim)

Answer in your own words, not mine. Short is fine. If an answer feels like
something you'd read rather than something you'd say, rewrite it.

---

**Q1. The three outcomes.**
psp-sim can answer 200, answer 503, or not answer at all. Two of those tell you
something and one tells you nothing. Which is which, and what is payments
allowed to write to the database in each case? Why is the third one dangerous
specifically because it *feels* like the second?

**Q2. After the Tom Scott video.**
The Two Generals' Problem says it is impossible to know whether your message
arrived over an unreliable network. Connect that to the `Idempotency-Key`
header you implemented on Day 3: if you can never be sure your request got
through, what does the client do, and why does that make idempotency a
*requirement* rather than a nice-to-have?

**Q3. `select` vs `time.Sleep`.**
You broke this on purpose: you replaced the `select` with `time.Sleep` and
watched `TestChargeStopsWhenCallerGivesUp` fail. Describe what actually went
wrong in terms of the goroutine and the connection — not "the test failed",
but what the server was *doing* for that extra second and who was listening.

**Q4. From `SRE thought process.txt`: what breaks if I delete this?**
Pick any one of these and answer concretely:
  - the `if c.jitter > 0` guard
  - the `* time.Millisecond` in `loadConfig`
  - the error check inside `envInt`
What is the failure, and would you notice it in production or would it just
quietly make your measurements wrong?

**Q5. Why `/healthz` ignores `errorRate`.**
psp-sim can be configured to fail 100% of charges. `/healthz` still returns
`ok`. Explain why that is correct, and what would happen during a game day if
health reflected the error rate instead.

---

## Numbers from tonight

| Thing | Value |
| --- | --- |
| Bring-up, one command | ~3 min |
| Smoke test (Day 3 contract) | 11/11 pass |
| psp-sim tests under `-race` | 10 functions, 2.0 s |
| Cluster footprint, 3 nodes to 1 | 2.5 GiB to 2.2 GiB |
| CI, after billing unlock | green in 1m17s |

## Still open

- Part B: outcome handling, `context.WithoutCancel`, 202 on timeout, ADR-003.
- `migrations/002_psp.sql` and the psp-sim deploy manifest (Block A2).
- ADR-001 and ADR-002 are skeletons with unfilled placeholders.
- Decision for next session: who types the Part B outcome logic.
