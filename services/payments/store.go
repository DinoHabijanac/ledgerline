package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")

type Payment struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
	// Empty until the provider authorizes the payment. omitempty keeps it out
	// of the JSON entirely for pending and failed payments, rather than
	// publishing a misleading "psp_ref": "".
	PSPRef string `json:"psp_ref,omitempty"`
}

// outcome is what the provider told us, translated into the two rows we have
// to write: the new payment status and the event other services will see.
type outcome struct {
	Status string // authorized | failed
	PSPRef string // set only for authorized
	Event  string // payment.authorized | payment.failed
}

var (
	outcomeAuthorized = func(ref string) outcome {
		return outcome{Status: "authorized", PSPRef: ref, Event: "payment.authorized"}
	}
	outcomeFailed = outcome{Status: "failed", Event: "payment.failed"}
)

type Store struct{ pool *pgxpool.Pool }

// CreatePayment returns the payment, whether it was newly created, and an error.
func (s *Store) CreatePayment(ctx context.Context, key, reqHash string, req createPaymentRequest) (Payment, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Payment{}, false, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	var p Payment
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (idempotency_key, request_hash, account_id, amount_cents, currency, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id::text, account_id, amount_cents, currency, status, COALESCE(psp_ref, '')`,
		key, reqHash, req.AccountID, req.AmountCents, req.Currency,
	).Scan(&p.ID, &p.AccountID, &p.AmountCents, &p.Currency, &p.Status, &p.PSPRef)

	if errors.Is(err, pgx.ErrNoRows) {
		// Key already used: replay the original result if the request is identical.
		// COALESCE because psp_ref is NULL for pending and failed payments, and
		// scanning a NULL into a Go string is an error rather than "".
		var storedHash string
		err = tx.QueryRow(ctx, `
			SELECT id::text, account_id, amount_cents, currency, status,
			       COALESCE(psp_ref, ''), request_hash
			FROM payments WHERE idempotency_key = $1`, key,
		).Scan(&p.ID, &p.AccountID, &p.AmountCents, &p.Currency, &p.Status, &p.PSPRef, &storedHash)
		if err != nil {
			return Payment{}, false, err
		}
		if storedHash != reqHash {
			return Payment{}, false, ErrIdempotencyConflict
		}
		return p, false, nil
	}
	if err != nil {
		return Payment{}, false, err
	}

	payload, err := json.Marshal(p)
	if err != nil {
		return Payment{}, false, err
	}
	// Same transaction: the payment and its event exist together or not at all.
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, event_type, payload)
		VALUES ($1::uuid, 'payment.created', $2)`, p.ID, payload); err != nil {
		return Payment{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Payment{}, false, err
	}
	return p, true, nil
}

// SetOutcome moves a pending payment to its terminal state and emits the
// matching event, both in one transaction.
//
// Two details carry all the weight here:
//
//  1. `WHERE status = 'pending'` makes this safe to run twice. If the row has
//     already left pending -- a retry, a concurrent request, a future
//     reconciliation job -- the UPDATE matches nothing, we emit no event, and
//     we return nil. Without that clause a double call would overwrite a
//     terminal status and publish a duplicate event.
//
//  2. The status change and the event are in the same transaction, for the
//     same reason as ADR-002: a payment that is authorized while no
//     payment.authorized event exists would be invisible to every downstream
//     service, including the ledger.
func (s *Store) SetOutcome(ctx context.Context, id string, o outcome) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var p Payment
	err = tx.QueryRow(ctx, `
		UPDATE payments
		SET status = $2, psp_ref = NULLIF($3, '')
		WHERE id = $1::uuid AND status = 'pending'
		RETURNING id::text, account_id, amount_cents, currency, status,
		          COALESCE(psp_ref, '')`,
		id, o.Status, o.PSPRef,
	).Scan(&p.ID, &p.AccountID, &p.AmountCents, &p.Currency, &p.Status, &p.PSPRef)

	if errors.Is(err, pgx.ErrNoRows) {
		// Already terminal. Someone got here first and published the event.
		// Doing nothing is the correct, idempotent answer.
		return nil
	}
	if err != nil {
		return err
	}

	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, event_type, payload)
		VALUES ($1::uuid, $2, $3)`, p.ID, o.Event, payload); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
