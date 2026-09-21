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

	"evenr/internal/aggregator"
	"evenr/internal/httpapi"
	"evenr/internal/planner"
	"evenr/internal/providers"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	p := planner.New(&aggregator.Aggregator{
		Showtimes:   &providers.MockShowtimes{},
		Restaurants: &providers.MockRestaurants{},
		Travel:      &providers.MockTravel{},
	})
	if v := os.Getenv("PLAN_BUDGET"); v != "" {
		budget, err := time.ParseDuration(v)
		if err != nil || budget <= 0 {
			logger.Error("PLAN_BUDGET must be a positive duration such as 300ms", "value", v)
			os.Exit(1)
		}
		p.Budget = budget
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.New(p, logger),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server starting", "addr", addr, "plan_budget", p.Budget.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
