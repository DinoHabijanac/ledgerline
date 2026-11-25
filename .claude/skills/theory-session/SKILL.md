---
name: theory-session
description: Run the theory session that follows a Ledgerline build session - one core idea, a reading order through the session's own files, then verified outside material. Use when the user asks for the theory session, wants to understand what was built, or says "explain today's session".
---

# Theory session

The user learns by reading after building; practice alone leaves them confused. This session is for understanding, not changing code: no edits, no commands that change anything. Reading files and `git log`/`git show` is fine.

## Inputs

Find the build session to cover: the latest row in `docs/PROGRESS.md`, its PR (`gh pr list --state merged -L 3`), its ADR, its `labs/day-NN.md`, and the user's `topics-covered.txt` (path in `CLAUDE.local.md`). Skip anything `topics-covered.txt` marks as done. Don't re-teach it.

## 1. One idea first

Name the single concept the session hinges on, in two or three sentences, before any file. Example: "A timeout is not a failure: the provider may have charged, so the payment stays pending and publishes nothing."

## 2. Files to read, in order

Concept first, then mechanism:

1. the session's ADR
2. the code, smallest file first
3. scripts and evidence
4. `labs/day-NN.md` last, as the self-check

For each file give: the question it answers, the one or two exact things to look at (a function, a comment, a line), and minutes to spend. End with a rough total time.

Tie the code to the three SRE questions where they fit: what breaks if this is deleted, what happens when it's slow or never answers, what happens if it runs twice or a thousand times at once.

## 3. Outside material, after the files

- Short videos first. Segments of Martin Kleppmann's Cambridge *Distributed Systems* lectures fit well; give the lecture and the minute range.
- Then short primary sources: papers, specs, official docs, the Google SRE books (a chapter or section, not a whole book).
- Tie every item to something in today's code ("this is why `SetOutcome` uses ...").
- **Verify before recommending.** Search for each item and check the title, link and length. Never invent them. If no good video turns up, say so rather than padding the list.

Afterwards, add each new item to `topics-covered.txt` marked `[assigned]`.

## 4. When the user brings exercises

Teach the method, not just the answer. Their weak spot is turning a problem into precise pseudocode, so:
1. Have them state the inputs, outputs and invariant in one line each.
2. Write the pseudocode together, step by step, asking what goes next before giving it.
3. Only then compare with a full solution, and name the step they would have missed.

When they answer a question in prose, push for precision: a mechanism and a number, not an adjective.
