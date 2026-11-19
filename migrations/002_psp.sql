-- Day 4: payments now call an external provider, so a payment needs somewhere
-- to record which charge at that provider it corresponds to.
--
-- Nullable on purpose. A payment has no psp_ref while it is still 'pending',
-- and a payment whose charge failed never gets one. Only the 'authorized'
-- path fills it in. Making this NOT NULL would force us to invent a
-- placeholder value, and a placeholder in a money column is how you end up
-- reconciling against the string 'unknown'.
ALTER TABLE payments ADD COLUMN psp_ref text;

-- Reconciliation (not built yet) will need to answer two questions:
--   "which payments are stuck pending and need chasing?"  -> this index
--   "does this provider charge exist in our ledger?"       -> the ref index
--
-- Partial index: only pending rows are interesting, and they are a small
-- minority once the system is healthy, so the index stays tiny. Same trick as
-- outbox_unpublished in 001.
CREATE INDEX payments_pending ON payments (created_at) WHERE status = 'pending';

-- Not UNIQUE. A unique constraint here would be a guess about the provider's
-- behaviour that we cannot enforce or verify, and a surprise duplicate would
-- then break the write path rather than merely being visible. Keep it an
-- index, let reconciliation detect duplicates instead.
CREATE INDEX payments_psp_ref ON payments (psp_ref) WHERE psp_ref IS NOT NULL;
