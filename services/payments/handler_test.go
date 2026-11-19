package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeStore implements paymentStore without a database: it returns whatever we tell it to.
type fakeStore struct {
	payment Payment
	created bool
	err     error

	// recorded by SetOutcome, so tests can assert what we decided to write
	mu           sync.Mutex
	outcomeCalls []outcome
	outcomeErr   error
}

func (f *fakeStore) CreatePayment(ctx context.Context, key, reqHash string, req createPaymentRequest) (Payment, bool, error) {
	return f.payment, f.created, f.err
}

func (f *fakeStore) SetOutcome(ctx context.Context, id string, o outcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomeCalls = append(f.outcomeCalls, o)
	return f.outcomeErr
}

func (f *fakeStore) outcomes() []outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]outcome(nil), f.outcomeCalls...)
}

// fakePSP is the only practical way to produce all three provider outcomes on
// demand. Causing a real timeout in a unit test would mean a slow test; causing
// a real connection reset would mean a flaky one.
type fakePSP struct {
	ref string
	err error

	mu    sync.Mutex
	calls int
	keys  []string
}

func (f *fakePSP) Charge(ctx context.Context, idempotencyKey string, p Payment) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.keys = append(f.keys, idempotencyKey)
	return f.ref, f.err
}

func (f *fakePSP) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestHandler(store paymentStore, psp pspCharger) *handler {
	return &handler{
		store:  store,
		psp:    psp,
		logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

func doRequest(h *handler, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.createPayment(rec, req)
	return rec
}

const validBody = `{"account_id":"acc-1","amount_cents":1500,"currency":"EUR"}`

// --- validation and idempotency (unchanged from Day 3) ---------------------

func TestCreatePayment(t *testing.T) {
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
			h := newTestHandler(tc.store, &fakePSP{ref: "psp_abc"})

			// ACT
			rec := doRequest(h, tc.key, tc.body)

			// ASSERT
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// --- the three provider outcomes, which is what Day 4 is about -------------

func TestCreatePaymentProviderOutcomes(t *testing.T) {
	tests := []struct {
		name string

		pspRef string
		pspErr error

		wantStatus   int
		wantOutcomes []outcome // what we expect to be written to the database
		wantBodyRef  string
	}{
		{
			name:         "charged -> authorized, 201, ref stored",
			pspRef:       "psp_deadbeefdeadbeef",
			pspErr:       nil,
			wantStatus:   http.StatusCreated,
			wantOutcomes: []outcome{{Status: "authorized", PSPRef: "psp_deadbeefdeadbeef", Event: "payment.authorized"}},
			wantBodyRef:  "psp_deadbeefdeadbeef",
		},
		{
			// 503 is a promise that nothing was charged, so we may safely
			// record a terminal failure.
			name:         "503 -> failed, 201, no ref",
			pspErr:       errPSPUnavailable,
			wantStatus:   http.StatusCreated,
			wantOutcomes: []outcome{{Status: "failed", Event: "payment.failed"}},
			wantBodyRef:  "",
		},
		{
			// THE important case. A timeout is not a failure: the charge may
			// have happened. Nothing may be written, and the answer is 202.
			name:         "timeout -> pending, 202, nothing written",
			pspErr:       errPSPUnknown,
			wantStatus:   http.StatusAccepted,
			wantOutcomes: nil,
			wantBodyRef:  "",
		},
		{
			name:         "unexpected provider error -> treated as unknown",
			pspErr:       errors.New("connection reset by peer"),
			wantStatus:   http.StatusAccepted,
			wantOutcomes: nil,
			wantBodyRef:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// ARRANGE
			store := &fakeStore{payment: Payment{ID: "p1", Status: "pending"}, created: true}
			h := newTestHandler(store, &fakePSP{ref: tc.pspRef, err: tc.pspErr})

			// ACT
			rec := doRequest(h, "k1", validBody)

			// ASSERT: the HTTP answer
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			// ASSERT: what we decided to persist. This matters more than the
			// status code -- it is the money.
			got := store.outcomes()
			if len(got) != len(tc.wantOutcomes) {
				t.Fatalf("SetOutcome called %d times, want %d (%+v)", len(got), len(tc.wantOutcomes), got)
			}
			for i := range got {
				if got[i] != tc.wantOutcomes[i] {
					t.Errorf("outcome[%d] = %+v, want %+v", i, got[i], tc.wantOutcomes[i])
				}
			}

			// ASSERT: the psp_ref we hand back to the client
			var body Payment
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("response not JSON: %v", err)
			}
			if body.PSPRef != tc.wantBodyRef {
				t.Errorf("psp_ref = %q, want %q", body.PSPRef, tc.wantBodyRef)
			}
		})
	}
}

// A replay must never reach the provider. If it did, a client retrying after a
// lost response would be charged twice -- exactly the Two Generals failure the
// idempotency key exists to prevent.
func TestReplayNeverCallsProvider(t *testing.T) {
	store := &fakeStore{payment: Payment{ID: "p1", Status: "authorized", PSPRef: "psp_1"}, created: false}
	psp := &fakePSP{ref: "psp_SHOULD_NOT_HAPPEN"}
	h := newTestHandler(store, psp)

	rec := doRequest(h, "k1", validBody)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := psp.callCount(); n != 0 {
		t.Errorf("provider called %d times on a replay, want 0", n)
	}
	if n := len(store.outcomes()); n != 0 {
		t.Errorf("SetOutcome called %d times on a replay, want 0", n)
	}
}

// The idempotency key must reach the provider, otherwise it cannot deduplicate
// on its side and we have no basis for ever retrying safely.
func TestIdempotencyKeyIsForwarded(t *testing.T) {
	psp := &fakePSP{ref: "psp_1"}
	h := newTestHandler(&fakeStore{payment: Payment{ID: "p1"}, created: true}, psp)

	doRequest(h, "key-xyz", validBody)

	psp.mu.Lock()
	defer psp.mu.Unlock()
	if len(psp.keys) != 1 || psp.keys[0] != "key-xyz" {
		t.Errorf("provider received keys %v, want [key-xyz]", psp.keys)
	}
}

// Charged the card, then failed to write it down. This must not report success:
// our database still says pending, so the honest answer is 202 and a loud log.
func TestChargedButOutcomeWriteFails(t *testing.T) {
	store := &fakeStore{
		payment:    Payment{ID: "p1", Status: "pending"},
		created:    true,
		outcomeErr: errors.New("database is on fire"),
	}
	h := newTestHandler(store, &fakePSP{ref: "psp_1"})

	rec := doRequest(h, "k1", validBody)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}

// context.WithoutCancel, proven. The client is already gone when the handler
// runs, and the outcome must still be recorded -- otherwise we would charge a
// card and lose the record of it because someone closed a laptop lid.
func TestOutcomeIsRecordedEvenIfClientDisconnects(t *testing.T) {
	store := &fakeStore{payment: Payment{ID: "p1", Status: "pending"}, created: true}
	h := newTestHandler(store, &fakePSP{ref: "psp_1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client has hung up before we even start

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(validBody)).WithContext(ctx)
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()

	h.createPayment(rec, req)

	got := store.outcomes()
	if len(got) != 1 || got[0].Status != "authorized" {
		t.Fatalf("outcome not recorded for a disconnected client: %+v", got)
	}
}
