# Day 4, Part A — the third service (psp-sim)

Answers below are Claude's, written on request. Q1–Q3 are short because I said
I already knew them; Q4 and Q5 are the ones I asked to have worked through.
If I reread this later and an answer sounds like something I read rather than
something I'd say, that's the signal to rewrite it in my own words.

---

**Q1. The three outcomes.**

`200` and `503` are information. Silence is not.

| Outcome | What payments knows | What it may write |
| --- | --- | --- |
| `200 + psp_ref` | the card was charged | `authorized`, store the ref |
| `503` | the card was not charged | `failed` |
| timeout / conn error | nothing | nothing terminal — stay `pending` |

The third is dangerous precisely because it *feels* like the second. Both
arrive as "an error" in the calling code, and the lazy move is to catch them
together and mark the payment failed. But a 503 is a promise that no money
moved, while a timeout is the absence of any promise — the charge may well have
succeeded on the provider's side while the response was lost. Collapse the two
and you eventually mark a charged payment `failed`, then reconciliation
re-charges the customer. The whole point of the design is that those two paths
stay separate.

**Q2. Two Generals and the idempotency key.**

If you can never be certain your request arrived, the only safe client
behaviour is to **retry**. And a client that retries will sometimes send the
same request twice for one real-world intent — once where the original was
genuinely lost, once where the original succeeded but the reply was lost.

The server can't distinguish those two cases either. So the responsibility
moves: the client attaches a key that names the *intent*, and the server
guarantees that one key produces at most one effect, no matter how many times
it arrives. That's `Idempotency-Key` with the `ON CONFLICT (idempotency_key)
DO NOTHING` from Day 3.

So idempotency isn't a nice-to-have robustness feature. Two Generals proves
retries are unavoidable, and once retries are unavoidable, deduplication on the
server is the only thing standing between you and double charges. It's a
*requirement* derived from an impossibility proof.

**Q3. `select` vs `time.Sleep`.**

With `Sleep`, the handler's goroutine is parked for the full delay and nothing
can wake it. At t=2000ms payments' client timeout fires and the connection goes
away, but the goroutine sleeps on until t=3000ms, then carefully serialises a
JSON response and writes it into a socket with no reader. One goroutine, one
connection and one timer held for a second after the last person who cared
stopped caring — and under load that's a goroutine per abandoned request, all
piling up.

With `select`, `r.Context().Done()` closes the moment net/http sees the
disconnect, the second case fires, and the handler returns at t=2000ms having
written nothing.

---

**Q4. What breaks if I delete this?**

The useful ranking here isn't "how broken is the code" — it's **how fast would
I find out**. All three of these are one-line deletions; they fail in very
different ways.

**(a) `if c.jitter > 0` — fails LOUDLY.**
`rand.N(0)` panics. But the process does *not* die: net/http recovers panics
per connection, logs the stack trace and closes that connection. So the pod
stays Running, liveness keeps passing, Kubernetes is perfectly content — and
every single charge fails. Default `PSP_JITTER_MS` is `0`, so this breaks
100% of requests in the default configuration, which means I'd hit it on the
very first curl.

Worth noticing what the caller sees: not a 503, but a closed connection. From
payments' point of view that's a transport error, which is the **unknown**
branch. So deleting this guard quietly routes every payment into the one
outcome I'm least equipped to handle. And a pod that panics on every request
while reporting itself healthy is a good reminder that "Running" is not a
synonym for "working".

**(b) `* time.Millisecond` in `loadConfig` — fails SILENTLY, and this is the worst one.**
`time.Duration(300)` is 300 *nanoseconds*, not 300ms. A thousandfold error.
Nothing panics, nothing logs, every test still passes except
`TestLoadConfigParsesUnits`.

The damage is to the evidence, not the code. Every game day I run is designed
to answer "what does this system do when the provider is slow?" — and with this
bug the provider is never slow. The 2s client timeout never fires, the unknown
path is never exercised, and I write a postmortem concluding the system handles
provider latency gracefully. The number is wrong, the conclusion is wrong, and
it's published. For a project whose entire output is measured evidence (G3, G4,
the Evidence Briefs), a silently wrong measurement is worse than a crash.

**(c) the error check in `envInt` — fails SILENTLY, same family as (b).**
`strconv.Atoi("1OO")` returns `(0, err)`. Drop the check and a typo'd manifest
becomes `0ms` latency rather than falling back to the 50ms default. Same
failure mode as (b): the experiment runs, produces numbers, and the numbers are
meaningless.

**The lesson:** a loud bug is a cheap bug. (a) costs me ten minutes. (b) and (c)
cost me a false public artifact and the credibility that goes with it. That's
why `TestLoadConfigParsesUnits` exists even though it looks trivial — it's the
only thing in the suite that catches a class of bug I would otherwise never
notice.

---

**Q5. Why `/healthz` ignores `errorRate`.**

Because a psp-sim failing 100% of charges is **not sick — it's doing its job
perfectly.** The error rate is configured, intentional behaviour, not a fault.
Liveness asks "is this process alive and making progress?", not "do I like the
answers it's giving?"

What would actually happen if health reflected `errorRate`, during a real game
day:

1. I set `PSP_ERROR_RATE=1` to test the failure path.
2. `/healthz` starts returning 503.
3. The liveness probe fails `failureThreshold` times; the kubelet kills the
   container.
4. It restarts — but `PSP_ERROR_RATE=1` lives in the Deployment spec, so it
   comes back sick and fails again. **CrashLoopBackOff.**

The experiment destroys itself. But the subtle part is worse than the obvious
part: the failure mode *changes underneath me mid-experiment*. Before the
restart, payments receives fast, clean 503s — the "definitely not charged"
path. During the restart and backoff, there is no listener at all, so payments
gets connection-refused and timeouts — the **unknown** path. My experiment
silently stops testing the branch I meant to test and starts testing a
different one, and the trace data will look like a provider outage rather than
provider rejections. I'd draw the wrong conclusion from data that looks fine.

The general rule, same as the one already in `day-01.md`: **liveness checks
local process health only.** Two corollaries learned today — never let a probe
depend on behaviour that is *intentionally* variable, and never let it depend
on a downstream dependency.

Related: psp-sim deliberately has no `/readyz`. It has no dependencies, so
readiness would be identical to liveness and would add nothing but noise.
Compare with payments, where `/readyz` pings the DB and `/healthz` deliberately
does not — same principle, applied to a service that *does* have a dependency.

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
