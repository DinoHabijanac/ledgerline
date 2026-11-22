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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	SetOutcome(ctx context.Context, id string, o outcome) error
}

type handler struct {
	store  paymentStore
	psp    pspCharger
	logger *slog.Logger
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
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

	// A replay. The original request already dealt with the provider, so
	// calling it again could charge the card a second time. Return what we
	// have and stop. This is the rule that makes the retries Two Generals
	// forces on our clients safe.
	if !created {
		writeJSON(w, http.StatusOK, p)
		return
	}

	// --------------------------------------------------------------------
	// Everything below happens exactly once per payment, and it is the only
	// place in Ledgerline that leaves the database's reach.
	// --------------------------------------------------------------------

	// WithoutCancel keeps the request's values (and later its trace) but drops
	// its cancellation. Once we have decided to charge a card, abandoning
	// halfway because the client hung up would turn a knowable outcome into an
	// unknown one -- the most expensive state in this system. The client
	// leaving does not un-charge the card, so we see the work through.
	// The 2s client timeout, not the caller's patience, is what bounds this.
	ctx := context.WithoutCancel(r.Context())

	ref, chargeErr := h.psp.Charge(ctx, key, p)

	// Label this request's span with the business result, so Jaeger can answer
	// "show me every payment whose outcome was unknown" directly. HTTP status
	// alone cannot: 201 covers both authorized and failed.
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("payment.id", p.ID))

	switch {
	// ---- charged: the provider told us it moved the money ----
	case chargeErr == nil:
		span.SetAttributes(attribute.String("payment.outcome", "authorized"))
		if err := h.store.SetOutcome(ctx, p.ID, outcomeAuthorized(ref)); err != nil {
			// We charged the card but could not record it. Do NOT report
			// success: the payment is still pending in our database and
			// reconciliation has to settle it.
			h.logger.Error("charged but could not record outcome",
				"payment_id", p.ID, "psp_ref", ref, "err", err)
			writeJSON(w, http.StatusAccepted, p)
			return
		}
		p.Status, p.PSPRef = "authorized", ref
		writeJSON(w, http.StatusCreated, p)

	// ---- definitely not charged: the only error we may act on ----
	case errors.Is(chargeErr, errPSPUnavailable):
		span.SetAttributes(attribute.String("payment.outcome", "failed"))
		h.logger.Info("provider declined", "payment_id", p.ID)
		if err := h.store.SetOutcome(ctx, p.ID, outcomeFailed); err != nil {
			h.logger.Error("could not record failure", "payment_id", p.ID, "err", err)
			writeJSON(w, http.StatusAccepted, p)
			return
		}
		p.Status = "failed"
		// Still 201: the payment resource was created and now has a terminal
		// status of "failed". The HTTP status describes the resource, the body
		// describes the money.
		writeJSON(w, http.StatusCreated, p)

	// ---- unknown: a timeout, a reset, an unparseable 200 ----
	default:
		// The key lesson of Day 4: A TIMEOUT IS NOT A FAILURE.
		//
		// The charge may have succeeded. Writing "failed" here would let
		// reconciliation re-charge a customer who already paid. So we change
		// nothing: the row stays 'pending', no terminal event is published,
		// and we answer 202 Accepted -- "we have your request, the outcome is
		// not settled yet". Resolving it is reconciliation's job.
		span.SetAttributes(attribute.String("payment.outcome", "unknown"))
		// Recorded as an event, NOT as span status Error: payments did its job
		// correctly here. The uncertainty belongs to the network, not to us.
		span.RecordError(chargeErr)
		h.logger.Warn("provider outcome unknown, leaving payment pending",
			"payment_id", p.ID, "err", chargeErr)
		writeJSON(w, http.StatusAccepted, p)
	}
}
