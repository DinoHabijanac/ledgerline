package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStore implements paymentStore without a database: it returns whatever we tell it to.
type fakeStore struct {
	payment Payment
	created bool
	err     error
}

func (f *fakeStore) CreatePayment(ctx context.Context, key, reqHash string, req createPaymentRequest) (Payment, bool, error) {
	return f.payment, f.created, f.err
}

func TestCreatePayment(t *testing.T) {
	validBody := `{"account_id":"acc-1","amount_cents":1500,"currency":"EUR"}`

	tests := []struct {
		name       string
		key        string
		body       string
		store      *fakeStore
		wantStatus int
	}{
		{
			name:       "missing Idempotency-Key",
			key:        "",
			body:       validBody,
			store:      &fakeStore{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			key:        "k1",
			body:       `{not json`,
			store:      &fakeStore{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "zero amount",
			key:        "k1",
			body:       `{"account_id":"acc-1","amount_cents":0,"currency":"EUR"}`,
			store:      &fakeStore{},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "idempotency conflict",
			key:        "k1",
			body:       validBody,
			store:      &fakeStore{err: ErrIdempotencyConflict},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "new payment",
			key:        "k1",
			body:       validBody,
			store:      &fakeStore{payment: Payment{ID: "p1"}, created: true},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "replayed payment",
			key:        "k1",
			body:       validBody,
			store:      &fakeStore{payment: Payment{ID: "p1"}, created: false},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// ARRANGE
			h := &handler{
				store:  tc.store,
				logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(tc.body))
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()

			// ACT
			h.createPayment(rec, req)

			// ASSERT
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
