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
		var ce *configErr
		if errors.As(err, &ce) {
			_, _ = fmt.Fprintln(os.Stderr, "zorgscope:", ce.err)
			os.Exit(2)
		}
		slog.Error("zorgscope failed", "err", err)
		os.Exit(1)
	}
}

// configErr marks any error returned by the config.Load step so main always treats it as a config
// error (arc42 §8.7: "config errors → exit code 2 with message"), not just the *config.ValidationError
// case (bad YAML, a failed validation rule). Without this, a missing or misconfigured
// ZORGSCOPE_CONFIG path — the single most likely operator mistake in a container with a bad mount —
// falls through config.Load as a raw *fs.PathError from os.Open, which carries no "config error"
// framing of its own and would otherwise hit the generic slog.Error+exit(1) branch below. Wrapping
// (rather than replacing) the error preserves errors.Is/errors.As access to the underlying cause —
// e.g. errors.Is(err, os.ErrNotExist) — which Task 7's review specifically wanted kept working on
// config.Load's return value.
type configErr struct{ err error }

func (e *configErr) Error() string { return e.err.Error() }
func (e *configErr) Unwrap() error { return e.err }

func run() error {
	cfgPath := os.Getenv("ZORGSCOPE_CONFIG")
	if cfgPath == "" {
		cfgPath = "config/zorgscope.yaml"
	}
	runtimeCfgPath := os.Getenv("ZORGSCOPE_RUNTIME_CONFIG")
	if runtimeCfgPath == "" {
		runtimeCfgPath = "/data/zorgscope.yaml"
	}
	secretPath := os.Getenv("ZORGSCOPE_SECRET_STORE")
	if secretPath == "" {
		secretPath = "/data/zorgscope.secrets"
	}
	cfgManager, err := config.NewManager(config.ManagerOptions{BootstrapPath: cfgPath, RuntimePath: runtimeCfgPath, SecretPath: secretPath, Getenv: os.Getenv})
	if err != nil {
		return &configErr{err}
	}
	cfg, _ := cfgManager.Current()
	redactor := logging.NewSecretSet(configSecretValues(cfg))
	log := logging.NewDynamic(os.Stdout, cfg.LogLevel, redactor)
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
	runtime := app.NewRuntime(store, clk, reg, &http.Client{Timeout: 30 * time.Second}, book, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtime.Start(ctx, cfg); err != nil {
		return err
	}
	defer runtime.Close()
	srv, err := server.New(server.Deps{Runtime: runtime, Config: cfgManager, Redactor: redactor, Store: store, Clock: clk, Cfg: cfg, Log: log,
		Ready: func() bool { return true }})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port), Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Info("zorgscope listening", "addr", httpSrv.Addr, "base_url", cfg.Server.BaseURL, "auth_mode", cfg.AuthMode)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stop() // also cancel ctx so sched/snap stop promptly on an unexpected listener error
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Info("shutting down")
	err = httpSrv.Shutdown(shutdownCtx)
	return err
}

func configSecretValues(cfg *config.Config) []string {
	return []string{cfg.Secrets.GitHubToken, cfg.Secrets.PlausibleAPIKey, cfg.Secrets.SessionSecret,
		cfg.Secrets.EnrollToken, cfg.Secrets.APIToken, cfg.Secrets.ConfigKey}
}
