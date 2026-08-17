// Command fakesources serves fixture-backed stand-ins for GitHub, Plausible and Todoist, so that
// each adapter (Tasks 7-10) can be developed and tested against a real HTTP server instead of a
// live network dependency and a real credential (FR-9.2). `make fakes` runs this on
// http://localhost:9090.
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

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := ":" + envOr("PORT", "9090")
	srv := &http.Server{
		Addr:              addr,
		Handler:           fakesources.NewServer(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("fakesources listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}
}

// envOr returns the environment variable key, or fallback if it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
