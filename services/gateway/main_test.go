package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHealthz(t *testing.T) {
	// 1. ARRANGE – build everything the code needs
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	// 2. ACT – call the thing you're testing, once
	newMux(nil).ServeHTTP(rec, req)

	// 3. ASSERT – check what came out
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d,want%d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

// Health must keep working even when the proxy could not be built, because a
// gateway that crash-loops on a bad PAYMENTS_URL is harder to fix than one
// that stays up and complains.
func TestHealthzWorksWithoutProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newMux(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// --- proxying ---------------------------------------------------------------

// The happy path: the request reaches payments unchanged, and payments'
// response and status code come back untouched.
func TestProxyForwardsRequestAndResponse(t *testing.T) {
	var gotPath, gotKey, gotBody, gotMethod string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotKey, gotBody, gotMethod = r.URL.Path, r.Header.Get("Idempotency-Key"), string(b), r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"p1","status":"authorized"}`))
	}))
	defer upstream.Close()

	proxy, err := newPaymentsProxy(upstream.URL, 3*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("newPaymentsProxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/payments",
		strings.NewReader(`{"account_id":"a","amount_cents":100,"currency":"EUR"}`))
	req.Header.Set("Idempotency-Key", "k-1")
	rec := httptest.NewRecorder()
	newMux(proxy).ServeHTTP(rec, req)

	if gotMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/payments" {
		t.Errorf("upstream path = %q, want /v1/payments", gotPath)
	}
	// If the gateway dropped this header, every payment would look unique to
	// payments and idempotency would silently stop working.
	if gotKey != "k-1" {
		t.Errorf("upstream Idempotency-Key = %q, want k-1", gotKey)
	}
	if !strings.Contains(gotBody, "amount_cents") {
		t.Errorf("upstream body = %q, want the JSON payload", gotBody)
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "authorized") {
		t.Errorf("body = %q, want the upstream body", rec.Body.String())
	}
}

// A 202 from payments -- the unknown-outcome answer -- must reach the client
// intact. If the gateway flattened it into a 500, the entire point of
// answering 202 would be lost.
func TestProxyPreserves202(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"id":"p1","status":"pending"}`))
	}))
	defer upstream.Close()

	proxy, _ := newPaymentsProxy(upstream.URL, 3*time.Second, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	newMux(proxy).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pending") {
		t.Errorf("body = %q, want pending", rec.Body.String())
	}
}

// Budget exceeded -> 504, not 502. The distinction matters during an incident:
// 504 means "we gave up waiting and do not know the outcome", 502 means
// "payments was not reachable at all".
func TestProxyBudgetExceededReturns504(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slower than the budget we give the proxy below. Kept short so
		// httptest's Close, which waits for in-flight handlers, does not drag
		// the suite out.
		select {
		case <-time.After(600 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()

	proxy, _ := newPaymentsProxy(upstream.URL, 100*time.Millisecond, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader("{}"))
	rec := httptest.NewRecorder()

	start := time.Now()
	newMux(proxy).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rec.Code)
	}
	// Proves the budget is what stopped it, not the upstream finishing.
	if elapsed > 400*time.Millisecond {
		t.Errorf("took %v, budget was 100ms", elapsed)
	}
}

// Upstream not listening at all -> 502.
func TestProxyUnreachableUpstreamReturns502(t *testing.T) {
	// A server we close immediately, so the port is almost certainly dead.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := dead.URL
	dead.Close()

	proxy, _ := newPaymentsProxy(addr, 3*time.Second, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	newMux(proxy).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// GET on the payments route is not proxied: the router rejects it first.
func TestProxyOnlyAcceptsPOST(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not have been reached")
	}))
	defer upstream.Close()

	proxy, _ := newPaymentsProxy(upstream.URL, time.Second, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/payments", nil)
	rec := httptest.NewRecorder()
	newMux(proxy).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// --- configuration ---------------------------------------------------------

func TestNewPaymentsProxyRejectsBadURLs(t *testing.T) {
	for _, target := range []string{"", "payments.ledgerline", "://nope"} {
		if _, err := newPaymentsProxy(target, time.Second, discardLogger()); err == nil {
			t.Errorf("newPaymentsProxy(%q) succeeded, want an error", target)
		}
	}
}
