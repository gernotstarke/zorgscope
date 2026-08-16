// Command zorgscope runs the dashboard: config → store → sources → scheduler → HTTP (arc42 §6.7).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/adapters/sqlite"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/logging"
	"github.com/gernotstarke/zorgscope/internal/server"
)

func main() {
	if err := run(); err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			_, _ = fmt.Fprintln(os.Stderr, "zorgscope:", err)
			os.Exit(2)
		}
		slog.Error("zorgscope failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := os.Getenv("ZORGSCOPE_CONFIG")
	if cfgPath == "" {
		cfgPath = "config/zorgscope.yaml"
	}
	cfg, err := config.Load(cfgPath, os.Getenv)
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, []string{cfg.Secrets.GitHubToken, cfg.Secrets.PlausibleAPIKey,
		cfg.Secrets.TodoistToken, cfg.Secrets.SessionSecret, cfg.Secrets.EnrollToken})
	slog.SetDefault(log)

	store, err := sqlite.Open(cfg.DataPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	clk := clock.Real{}
	book := app.NewCredentialBook(clk)
	reg := app.NewRegistry()
	registerSources(reg)
	sources, err := reg.Build(app.Deps{Cfg: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}, Sink: book, Log: log})
	if err != nil {
		return err
	}
	sched := app.NewScheduler(store, store, clk, log, time.Duration(cfg.UI.RefreshMinGapSeconds)*time.Second)
	for _, s := range sources {
		sched.Add(s.Fetcher, s.Interval)
	}
	snap := app.NewSnapshotter(store, store, store, sched.SourceIDs, cfg.Snapshot.Hour, cfg.Snapshot.Minute,
		cfg.Server.Location, cfg.Snapshot.RetentionDays, clk, log)
	dash := app.NewDashboard(store, clk, cfg)
	srv, err := server.New(server.Deps{Dashboard: dash, Refresher: sched, Store: store, Clock: clk, Cfg: cfg, Log: log,
		Ready: func() bool { return true }})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// sched.Run and snap.Run must fully return before this function returns and the deferred
	// store.Close() runs above: the scheduler's terminal status write for an in-flight fetch uses
	// a context detached from ctx's cancellation (internal/app/scheduler.go, statusWriteTimeout),
	// specifically so a shutdown signal doesn't cut it short. If run() returned as soon as ctx was
	// cancelled, main() would exit and the process would end while that detached write (up to 5 s)
	// was still in flight, truncating it. Waiting on background here — not just on
	// httpSrv.Shutdown — is what makes that write actually survive shutdown.
	var bg sync.WaitGroup
	bg.Add(2)
	go func() { defer bg.Done(); sched.Run(ctx) }()
	go func() { defer bg.Done(); snap.Run(ctx, time.Minute) }()

	httpSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port), Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Info("zorgscope listening", "addr", httpSrv.Addr, "base_url", cfg.Server.BaseURL, "sources", len(sources), "auth_mode", cfg.AuthMode)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stop() // also cancel ctx so sched/snap stop promptly on an unexpected listener error
		bg.Wait()
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Info("shutting down")
	err = httpSrv.Shutdown(shutdownCtx)
	bg.Wait()
	return err
}
