// Command psp-sim is a fake external payment provider.
//
// It exists so the rest of the platform can be tested against a dependency
// that is slow, flaky, or both, on demand. Everything it does is driven by
// environment variables, so a game day can make it sick with `kubectl set env`
// and no rebuild.
//
// CONTRACT, and the whole reason this service exists:
//
//	200 -> the card WAS charged; psp_ref identifies the charge
//	503 -> the card was NOT charged; safe to treat as a definite failure
//	no answer (timeout / connection error) -> UNKNOWN. The charge may or may
//	    not have happened. Callers must not guess.
//
// That third case is not an oversight. It is the interesting one.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DinoHabijanac/ledgerline/pkg/httpx"
	"github.com/DinoHabijanac/ledgerline/pkg/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// config is read once at startup and never mutated afterwards, so concurrent
// handlers can all read it without any locking.
type config struct {
	latency   time.Duration
	jitter    time.Duration
	errorRate float64
}

// envInt reads an integer env var, falling back to def when unset or garbage.
// Deliberately forgiving: a typo in a manifest should start the service with
// a sane default, not crash-loop a pod at 03:00.
func envInt(key string, def int) int {
	n, err := strconv.Atoi(httpx.EnvOr(key, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	f, err := strconv.ParseFloat(httpx.EnvOr(key, strconv.FormatFloat(def, 'f', -1, 64)), 64)
	if err != nil {
		return def
	}
	return f
}

func loadConfig() config {
	return config{
		latency:   time.Duration(envInt("PSP_LATENCY_MS", 50)) * time.Millisecond,
		jitter:    time.Duration(envInt("PSP_JITTER_MS", 0)) * time.Millisecond,
		errorRate: envFloat("PSP_ERROR_RATE", 0),
	}
}

type chargeResponse struct {
	PSPRef string `json:"psp_ref"`
	Status string `json:"status"`
}

func (c config) charge(w http.ResponseWriter, r *http.Request) {
	delay := c.latency
	if c.jitter > 0 {
		// rand.N is generic over integer types, and time.Duration is an int64
		// underneath, so this hands back a Duration directly. It panics on
		// n <= 0, which is why the guard above is not optional.
		delay += rand.N(c.jitter)
	}

	// Race the artificial latency against the caller giving up.
	//
	// time.Sleep would be wrong here. Sleep is unconditional: the goroutine is
	// gone for the full delay no matter what happens to the client. But the
	// client may already have left, because their own timeout fired or the
	// user closed the tab. r.Context() is cancelled in both cases, and
	// net/http closes its Done channel for us. Taking whichever channel fires
	// first means this service stops working the moment nobody is waiting.
	timer := time.NewTimer(delay)
	defer timer.Stop() // release the timer early when the context wins the race

	// The span otelhttp opened for this request. Annotating it means Jaeger
	// shows not just THAT psp-sim was slow, but how slow it was told to be.
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.Int64("psp.injected_delay_ms", delay.Milliseconds()))

	select {
	case <-timer.C:
		// slept the full delay, carry on and answer
	case <-r.Context().Done():
		// Caller hung up. No point writing to a connection that is going away,
		// and no point charging a card nobody is waiting to hear about.
		// The event makes this visible in the trace: a span that ends early
		// with this marker is a caller's timeout seen from the other side.
		span.AddEvent("caller gave up before the delay elapsed")
		return
	}

	// Failure injection. rand.Float64 returns [0,1), so errorRate 1 always
	// fires and errorRate 0 never does. Both extremes are deterministic, which
	// is what makes them testable.
	if rand.Float64() < c.errorRate {
		span.SetAttributes(attribute.Bool("psp.injected_failure", true))
		// 503 is a PROMISE: nothing was charged. payments leans on this to mark
		// a payment failed without risking a double charge later.
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chargeResponse{
		PSPRef: fmt.Sprintf("psp_%016x", rand.Uint64()),
		Status: "approved",
	})
}

// newMux is split out from main so tests can build the real routing table
// without starting a listener or binding a port.
func newMux(cfg config) *http.ServeMux {
	mux := http.NewServeMux()
	// Liveness only. This service has no dependencies, so "the process is up"
	// is the whole truth and there is no separate /readyz worth writing.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/charge", cfg.charge)
	return mux
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := loadConfig()

	// Log the effective config at startup. When a game day produces strange
	// numbers the first question is always "what was this thing actually
	// configured to do?", and this line answers it without a kubectl describe.
	logger.Info("psp-sim config",
		"latency_ms", cfg.latency.Milliseconds(),
		"jitter_ms", cfg.jitter.Milliseconds(),
		"error_rate", cfg.errorRate,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, "psp-sim")
	if err != nil {
		// Tracing is diagnostics, not the product: a broken collector config
		// must never stop the service from charging cards.
		logger.Error("tracing disabled", "err", err)
		shutdownTracing = func(context.Context) error { return nil }
	}

	srv := &http.Server{
		Addr: ":" + httpx.EnvOr("PORT", "8080"),
		// Order matters: telemetry outermost, so the span already exists when
		// LogRequests runs and the log line can carry its trace_id.
		Handler:           telemetry.Server(httpx.LogRequests(logger, newMux(cfg)), "psp-sim"),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("psp-sim listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	// After the server, not before: the last in-flight requests finish during
	// srv.Shutdown and their spans must still get flushed.
	if err := shutdownTracing(shutdownCtx); err != nil {
		logger.Error("tracing flush failed", "err", err)
	}
}
