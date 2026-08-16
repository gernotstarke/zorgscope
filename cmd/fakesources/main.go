// Command fakesources serves fake upstream APIs (GitHub in M1) for e2e tests and the local demo.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fakesources stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()
	gh := githubfake.New()
	githubfake.Seed(gh, time.Now())
	slog.Info("fakesources listening", "addr", *addr, "github", "/graphql, /repos/…/actions/runs, /notifications, /__control/*")
	srv := &http.Server{Addr: *addr, Handler: gh.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	slog.Info("shutting down")
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
