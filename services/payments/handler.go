package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

type createPaymentRequest struct {
	AccountID   string `json:"account_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

func (r createPaymentRequest) validate() error {
	switch {
	case r.AccountID == "":
		return errors.New("account_id is required")
	case r.AmountCents <= 0:
		return errors.New("amount_cents must be positive")
	case len(r.Currency) != 3:
		return errors.New("currency must be a 3-letter ISO 4217 code")
	}
	return nil
}

// paymentStore is an interface so tests can use a fake instead of Postgres.
type paymentStore interface {
	CreatePayment(ctx context.Context, key, reqHash string, req createPaymentRequest) (Payment, bool, error)
}

type handler struct {
	store  paymentStore
	logger *slog.Logger
}

func (h *handler) createPayment(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "missing Idempotency-Key header", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	var req createPaymentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	// Hash the normalised request, not the raw bytes, so whitespace differences don't matter.
	canonical, _ := json.Marshal(req)
	sum := sha256.Sum256(canonical)

	p, created, err := h.store.CreatePayment(r.Context(), key, hex.EncodeToString(sum[:]), req)
	switch {
	case errors.Is(err, ErrIdempotencyConflict):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		h.logger.Error("create payment failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	status := http.StatusOK // replay of an earlier request
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(p)
}
