package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"
)

// post builds a POST /v1/charge request and runs it straight through the mux,
// with no network and no listening socket. httptest.NewRecorder stands in for
// a real connection and remembers what the handler wrote.
func post(t *testing.T, cfg config, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/charge", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	newMux(cfg).ServeHTTP(rec, req)
	return rec
}

// --- the happy path: 200 means charged -------------------------------------

func TestChargeApproves(t *testing.T) {
	rec := post(t, config{}, context.Background())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got chargeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v (body %q)", err, rec.Body.String())
	}
	if got.Status != "approved" {
		t.Errorf("status = %q, want approved", got.Status)
	}
	// payments will store this string in a database column and an auditor may
	// one day have to match it against a provider statement, so pin the shape.
	if !regexp.MustCompile(`^psp_[0-9a-f]{16}$`).MatchString(got.PSPRef) {
		t.Errorf("psp_ref = %q, want psp_ + 16 hex digits", got.PSPRef)
	}
}

// --- failure injection, both extremes --------------------------------------

// rand.Float64 returns a value in [0,1). An error rate of 1 is therefore
// always greater, and an error rate of 0 never is, so neither case is flaky.
func TestChargeErrorRateExtremes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		errorRate float64
		want      int
	}{
		{"always fails", 1, http.StatusServiceUnavailable},
		{"never fails", 0, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Repeat: a single call passing proves very little about a
			// probabilistic branch.
			for i := 0; i < 50; i++ {
				rec := post(t, config{errorRate: tc.errorRate}, context.Background())
				if rec.Code != tc.want {
					t.Fatalf("call %d: status = %d, want %d", i, rec.Code, tc.want)
				}
			}
		})
	}
}

// --- "what happens when it's slow or never answers?" -----------------------

// The important one. A cancelled caller must abandon the handler immediately
// rather than burn the full configured latency.
//
// Without the select in charge(), this test would take the full second.
func TestChargeStopsWhenCallerGivesUp(t *testing.T) {
	const latency = time.Second
	const giveUpAfter = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), giveUpAfter)
	defer cancel()

	start := time.Now()
	rec := post(t, config{latency: latency}, ctx)
	elapsed := time.Since(start)

	// Generous upper bound: we only care that it returned early, not that it
	// returned in exactly 50ms. A tight bound here would flake on a loaded CI
	// runner, and a flaky test is worse than no test.
	if elapsed > latency/2 {
		t.Errorf("handler took %v, should have aborted near %v", elapsed, giveUpAfter)
	}
	// Nothing should have been written: there is no one left to read it.
	if rec.Body.Len() != 0 {
		t.Errorf("wrote %q to an abandoned connection", rec.Body.String())
	}
}

// Latency is actually applied, not silently ignored. This is the other half of
// the test above: proving the early return was caused by cancellation and not
// by the delay never happening in the first place.
func TestChargeAppliesLatency(t *testing.T) {
	const latency = 120 * time.Millisecond

	start := time.Now()
	rec := post(t, config{latency: latency}, context.Background())
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if elapsed < latency {
		t.Errorf("returned after %v, expected at least %v", elapsed, latency)
	}
}

// Jitter stays inside [latency, latency+jitter]. Guards the rand.N guard: if
// someone deletes the `if c.jitter > 0` check, rand.N(0) panics, and this test
// exercises the jitter == 0 path through the table above plus a nonzero one here.
func TestChargeJitterBounds(t *testing.T) {
	const latency = 20 * time.Millisecond
	const jitter = 40 * time.Millisecond

	for i := 0; i < 20; i++ {
		start := time.Now()
		post(t, config{latency: latency, jitter: jitter}, context.Background())
		elapsed := time.Since(start)

		if elapsed < latency {
			t.Fatalf("call %d: %v is below the floor %v", i, elapsed, latency)
		}
		// Upper bound doubled to absorb scheduler noise; we are checking that
		// jitter is bounded at all, not measuring the clock.
		if elapsed > 2*(latency+jitter) {
			t.Fatalf("call %d: %v far exceeds ceiling %v", i, elapsed, latency+jitter)
		}
	}
}

// --- "what happens if it runs a thousand times at once?" -------------------

// config is shared by every handler and math/rand/v2's top-level functions are
// goroutine-safe, so concurrent charges must not race or panic.
// Only meaningful under `go test -race`, which CI runs.
func TestChargeConcurrent(t *testing.T) {
	cfg := config{latency: time.Millisecond, jitter: time.Millisecond, errorRate: 0.5}

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(t, cfg, context.Background())
			if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
				t.Errorf("unexpected status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
}

// --- "what breaks if I delete this?" ---------------------------------------

// Unset and malformed env vars must both fall back to the default rather than
// crash the process or silently become zero.
func TestEnvHelpersFallBack(t *testing.T) {
	t.Run("int unset", func(t *testing.T) {
		if got := envInt("PSP_NOT_SET_AT_ALL", 50); got != 50 {
			t.Errorf("got %d, want 50", got)
		}
	})
	t.Run("int garbage", func(t *testing.T) {
		t.Setenv("PSP_LATENCY_MS", "not-a-number")
		if got := envInt("PSP_LATENCY_MS", 50); got != 50 {
			t.Errorf("got %d, want 50", got)
		}
	})
	t.Run("int valid", func(t *testing.T) {
		t.Setenv("PSP_LATENCY_MS", "250")
		if got := envInt("PSP_LATENCY_MS", 50); got != 250 {
			t.Errorf("got %d, want 250", got)
		}
	})
	t.Run("float garbage", func(t *testing.T) {
		t.Setenv("PSP_ERROR_RATE", "half")
		if got := envFloat("PSP_ERROR_RATE", 0); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("float valid", func(t *testing.T) {
		t.Setenv("PSP_ERROR_RATE", "0.25")
		if got := envFloat("PSP_ERROR_RATE", 0); got != 0.25 {
			t.Errorf("got %v, want 0.25", got)
		}
	})
}

// loadConfig turns milliseconds into Durations. An off-by-1000 here would make
// every game day lie to us, so it is worth one explicit test.
func TestLoadConfigParsesUnits(t *testing.T) {
	t.Setenv("PSP_LATENCY_MS", "300")
	t.Setenv("PSP_JITTER_MS", "40")
	t.Setenv("PSP_ERROR_RATE", "0.1")

	cfg := loadConfig()
	if cfg.latency != 300*time.Millisecond {
		t.Errorf("latency = %v, want 300ms", cfg.latency)
	}
	if cfg.jitter != 40*time.Millisecond {
		t.Errorf("jitter = %v, want 40ms", cfg.jitter)
	}
	if cfg.errorRate != 0.1 {
		t.Errorf("errorRate = %v, want 0.1", cfg.errorRate)
	}
}

// Health must never depend on the provider's simulated sickness: a psp-sim
// configured to fail every charge is still a live, healthy process, and
// Kubernetes must not restart it.
func TestHealthzIgnoresErrorRate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newMux(config{errorRate: 1, latency: time.Hour}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}

// Wrong method on the charge route must be rejected by the router, not reach
// the handler. Go 1.22+ pattern routing gives us this for free; the test makes
// sure nobody loosens the pattern later.
func TestChargeRejectsGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/charge", nil)
	rec := httptest.NewRecorder()
	newMux(config{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
