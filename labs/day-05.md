# Day 5 — first end-to-end trace (Gate 1)

**Q1. Propagation vs export.**
These are two separate jobs in `pkg/telemetry`. Which one makes three services'
spans into *one* trace, and which one makes them *visible*? If you deleted
`rp.Transport = telemetry.Transport(nil)` from the gateway, what would Jaeger
show for one payment, and would the payment itself still work?

**Q2. Choosing the trace ID yourself.**
`scripts/trace.sh` sends its own `traceparent` header. Explain what the gateway
does with it, and why this means curl is effectively "service zero". What does
each part of `00-<trace id>-<span id>-01` mean?

**Q3. The nested INSERT.**
The first traced payment had 19 spans, the second only 15. What were the four
extra spans, why did they disappear, and what did they cost in milliseconds?
What does this tell you about the latency of the first requests after a payments
pod restarts?

**Q4. `WithoutCancel` and the trace.**
On Day 4, the handler switched to `context.WithoutCancel(r.Context())`. Why
does the PSP call still show up as a *child* of the payments request span,
rather than starting a new trace? What exactly does `WithoutCancel` keep, and
what does it drop?

**Q5. What tracing must never do.**
Find the three places in today's code where a tracing failure is prevented from
affecting payments (hint: `Setup` errors, an unset env var, and how spans are
sent). Why is "tracing breaks payments" worse than "payments run untraced"?

---

## Numbers from today

| Thing | Value |
| --- | --- |
| Spans in one payment trace, cold connection | 19 |
| Spans in one payment trace, warm connection | 15 |
| End to end, warm | 72.1 ms |
| psp-sim share of that | 56.8 ms (79 %) |
| Postgres share | ~8 ms (11 %) |
| payment `INSERT`, cold vs warm | 6.9 ms vs 1.2 ms |
| Slow-provider round trip with tracing on | 2030 ms (smoke 27/27) |
