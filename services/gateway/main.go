package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DinoHabijanac/ledgerline/pkg/httpx"
	"github.com/DinoHabijanac/ledgerline/pkg/telemetry"
)

// withTimeout gives every proxied request a hard budget.
//
// TIMEOUT BUDGETS MUST SHRINK AS YOU GO DEEPER:
//
//	gateway  3s  ─┐
//	payments 2s   │ its http.Client timeout to psp-sim
//	psp-sim  ~0.1s│ its configured latency
//	              └─ each layer leaves room for the one inside it
//
// The outer budget must EXCEED the inner one. If the gateway gave up at 2s or
// less, it would cut payments off mid-charge: the client would get a generic
// gateway error, while payments (which deliberately uses
// context.WithoutCancel) carried on, charged the card and recorded an outcome
// the client never learned about. The customer sees a failure for a payment
// that actually succeeded.
//
// With 3s, payments' own 2s timeout fires first, so payments gets to decide
// what happened and answer honestly -- 202 for an unknown outcome. The gateway
// then just relays that meaningful answer. The inner service, which knows the
// most, is the one that gets to report.
//
// The spare second covers the database writes and two network hops.
func withTimeout(budget time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newPaymentsProxy(target string, budget time.Duration, logger *slog.Logger) (http.Handler, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("PAYMENTS_URL must be an absolute URL, e.g. http://payments.ledgerline")
	}

	rp := httputil.NewSingleHostReverseProxy(u)
	// The gateway is where most traces begin. Its transport creates the
	// "gateway -> payments" client span and sends `traceparent` onward, so
	// payments joins this trace instead of starting its own.
	rp.Transport = telemetry.Transport(nil)

	// Without this, a failed upstream call produces a bare 502 and a line on
	// the proxy's own logger, which is not ours. We want our JSON logs and we
	// want to distinguish "it took too long" from "it was not there".
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		status := http.StatusBadGateway // upstream refused, reset, or unresolvable
		if errors.Is(err, context.DeadlineExceeded) {
			// Our own budget ran out. 504 is the honest code: we gave up
			// waiting, we do not know what payments decided.
			status = http.StatusGatewayTimeout
		}
		logger.Error("proxy to payments failed",
			"err", err, "status", status, "target", target)
		http.Error(w, http.StatusText(status), status)
	}

	return withTimeout(budget, rp), nil
}

// newMux takes the proxy as an argument so it can be built in tests without a
// network. A nil proxy is allowed on purpose: if PAYMENTS_URL is misconfigured
// the gateway should still come up and answer /healthz rather than crash-loop,
// for the same reason liveness probes never check dependencies.
func newMux(paymentsProxy http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	if paymentsProxy != nil {
		mux.Handle("POST /v1/payments", paymentsProxy)
	}
	return mux
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	budgetMS, err := strconv.Atoi(httpx.EnvOr("PAYMENTS_TIMEOUT_MS", "3000"))
	if err != nil {
		budgetMS = 3000
	}
	target := httpx.EnvOr("PAYMENTS_URL", "http://payments.ledgerline")

	proxy, err := newPaymentsProxy(target, time.Duration(budgetMS)*time.Millisecond, logger)
	if err != nil {
		// Deliberately not fatal: serve health, log loudly, let the operator
		// fix the env var without the pod flapping in CrashLoopBackOff.
		logger.Error("payments proxy disabled", "err", err, "target", target)
		proxy = nil
	} else {
		logger.Info("payments proxy ready", "target", target, "budget_ms", budgetMS)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, "gateway")
	if err != nil {
		logger.Error("tracing disabled", "err", err)
		shutdownTracing = func(context.Context) error { return nil }
	}

	srv := &http.Server{
		Addr:              ":" + httpx.EnvOr("PORT", "8080"),
		Handler:           telemetry.Server(telemetry.TraceIDHeader(httpx.LogRequests(logger, newMux(proxy))), "gateway"),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("gateway listening", "addr", srv.Addr)
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
	if err := shutdownTracing(shutdownCtx); err != nil {
		logger.Error("tracing flush failed", "err", err)
	}
}
