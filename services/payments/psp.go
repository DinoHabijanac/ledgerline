package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/DinoHabijanac/ledgerline/pkg/telemetry"
)

// Two sentinel errors, because the caller must be able to tell these apart.
// This distinction is the entire point of Day 4.
var (
	// errPSPUnavailable means the provider explicitly said it did NOT charge.
	// A definite, trustworthy failure. Safe to mark the payment failed.
	errPSPUnavailable = errors.New("psp unavailable")

	// errPSPUnknown means we do not know whether the charge happened. A
	// timeout, a connection reset, a 200 we could not parse. The payment must
	// stay pending and be reconciled later. Anything that is not
	// errPSPUnavailable is treated as unknown, so this is really documentation
	// plus a wrapping target.
	errPSPUnknown = errors.New("psp outcome unknown")
)

// pspCharger lets the handler be tested with a fake provider that can produce
// all three outcomes on demand, including the ones that are hard to cause with
// a real network.
type pspCharger interface {
	Charge(ctx context.Context, idempotencyKey string, p Payment) (pspRef string, err error)
}

type pspClient struct {
	baseURL string
	http    *http.Client
}

func newPSPClient(baseURL string) *pspClient {
	return &pspClient{
		baseURL: baseURL,
		http: &http.Client{
			// A whole-request budget: connect, write, wait, read the body.
			// This is the single most important line in the file. Without it,
			// http.Client waits forever, and one sick provider turns into a
			// pile of stuck goroutines and exhausted DB connections here.
			//
			// 2s is deliberately less than the gateway's 3s budget, so this
			// timeout fires first and payments gets to record an outcome
			// before anyone upstream gives up on us.
			Timeout: 2 * time.Second,
			// Creates the "payments -> psp-sim" client span and injects
			// `traceparent`, so psp-sim's span lands in the same trace.
			// Because the handler uses context.WithoutCancel, the trace parent
			// survives even though cancellation is dropped: WithoutCancel
			// keeps the context's VALUES, and the span lives in a value.
			Transport: telemetry.Transport(nil),
		},
	}
}

type chargeRequest struct {
	AccountID   string `json:"account_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

type chargeResponse struct {
	PSPRef string `json:"psp_ref"`
	Status string `json:"status"`
}

// Charge calls the provider and maps its answer onto our three outcomes:
//
//	(ref, nil)                -> charged
//	("", errPSPUnavailable)   -> definitely not charged
//	("", anything else)       -> unknown, do not guess
func (c *pspClient) Charge(ctx context.Context, idempotencyKey string, p Payment) (string, error) {
	body, err := json.Marshal(chargeRequest{
		AccountID:   p.AccountID,
		AmountCents: p.AmountCents,
		Currency:    p.Currency,
	})
	if err != nil {
		// Our own bug, and nothing was sent, so nothing was charged.
		return "", fmt.Errorf("marshal charge request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/charge", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build charge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Forward the caller's idempotency key. A real provider uses it to
	// deduplicate, which is what makes a retry safe. We do not retry yet, but
	// sending the key is what earns us the right to later.
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		// Timeout, connection refused, reset, DNS failure. The request may
		// have reached the provider and been processed before the wire broke.
		// We cannot know. UNKNOWN.
		return "", fmt.Errorf("%w: %v", errPSPUnknown, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		var cr chargeResponse
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			// Subtle and important: the provider said 200, so the money very
			// likely moved, but we cannot read the reference. This is NOT a
			// failure. It is unknown, and it must not mark the payment failed.
			return "", fmt.Errorf("%w: undecodable 200 response: %v", errPSPUnknown, err)
		}
		if cr.PSPRef == "" {
			return "", fmt.Errorf("%w: 200 response with empty psp_ref", errPSPUnknown)
		}
		return cr.PSPRef, nil

	case http.StatusServiceUnavailable:
		// The contract. 503 is a promise that nothing was charged.
		// Drain the body so this connection can be reused from the pool.
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", errPSPUnavailable

	default:
		// 500, 502, 418, anything unexpected: no promise was made either way.
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: provider returned %d", errPSPUnknown, resp.StatusCode)
	}
}
