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
}

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
		RETURNING id::text, account_id, amount_cents, currency, status`,
		key, reqHash, req.AccountID, req.AmountCents, req.Currency,
	).Scan(&p.ID, &p.AccountID, &p.AmountCents, &p.Currency, &p.Status)

	if errors.Is(err, pgx.ErrNoRows) {
		// Key already used: replay the original result if the request is identical.
		var storedHash string
		err = tx.QueryRow(ctx, `
			SELECT id::text, account_id, amount_cents, currency, status, request_hash
			FROM payments WHERE idempotency_key = $1`, key,
		).Scan(&p.ID, &p.AccountID, &p.AmountCents, &p.Currency, &p.Status, &storedHash)
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
