package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DinoHabijanac/ledgerline/pkg/httpx"
	"github.com/DinoHabijanac/ledgerline/pkg/telemetry"
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, "payments")
	if err != nil {
		logger.Error("tracing disabled", "err", err)
		shutdownTracing = func(context.Context) error { return nil }
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Error("invalid DATABASE_URL", "err", err)
		os.Exit(1)
	}
	cfg.MaxConns = 5
	// Every query becomes a child span of whatever request ran it -- this is
	// the "-> Postgres" hop of Gate 1's trace. Span names are trimmed to
	// "INSERT payments" rather than the whole statement. Query PARAMETERS are
	// not recorded (the library's default), so account IDs and amounts never
	// leave the database inside a trace.
	//
	// No spans are created for queries without a traced parent: otelpgx checks
	// IsRecording() first, so the /readyz ping every 5s stays invisible.
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithTrimSQLInSpanName())
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		logger.Error("db pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	h := &handler{
		store:  &Store{pool: pool},
		psp:    newPSPClient(httpx.EnvOr("PSP_URL", "http://psp-sim.ledgerline")),
		logger: logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ready"))
	})
	mux.HandleFunc("POST /v1/payments", h.createPayment)

	srv := &http.Server{
		Addr:              ":" + httpx.EnvOr("PORT", "8080"),
		Handler:           telemetry.Server(httpx.LogRequests(logger, mux), "payments"),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("payments listening", "addr", srv.Addr)
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
