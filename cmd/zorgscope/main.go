// Command zorgscope serves the personal status dashboard.
//
// The process is stateless: the only thing it remembers between requests is the last fetched item
// list, held in memory by internal/snapshot and refetched when it is stale. There is no database,
// no background scheduler and no refresh pipeline — the Fly Machine it runs on is stopped whenever
// nothing is in flight, and the first page view after a cold start pays the one fetch (design §5).
//
// Everything is assembled in one place, run: configuration, the GitHub source, the snapshot cache
// and the HTTP server. Nothing below it reads the environment.
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

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
	"github.com/gernotstarke/zorgscope/internal/web"
)

// upstreamTimeout bounds one HTTP call to GitHub. It is shorter than the Machine's own patience so
// that a hanging upstream fails a fetch rather than holding a page view open indefinitely.
const upstreamTimeout = 20 * time.Second

// defaultCacheTTL is how old the snapshot may be before a page view refetches it, used when the
// configuration names none. Task 4 renames the config field this is read from
// (cfg.Refresh.Interval) to github.cache_ttl; until then this is where the default lives.
const defaultCacheTTL = 5 * time.Minute

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		// Every error reaching here has already been scrubbed of secrets where it could carry
		// one (QS-4.3).
		log.Error("zorgscope stopped", "err", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load(envOr("CONFIG_PATH", "config/zorgscope.yaml"), os.Getenv)
	if err != nil {
		// config.Load names the offending field and never quotes a secret's value.
		return fmt.Errorf("configuration: %w", err)
	}

	hc := &http.Client{Timeout: upstreamTimeout}
	fetcher := github.NewIssueFetcher(githubConfig(cfg), hc)
	src := ports.SourceFunc(func(ctx context.Context) ([]domain.Item, error) {
		r, err := fetcher.Fetch(ctx)
		return r.Items, err
	})

	ttl := cfg.Refresh.Interval
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	cache := snapshot.New(src, ttl, ports.SystemClock{})

	srv, err := web.New(web.Options{
		Config: cfg,
		Cache:  cache,
		Clock:  ports.SystemClock{},
		Log:    log,
		// Who may sign in is decided by GitHub, against the repository named in the
		// configuration file (FR-8.3).
		Access:     github.NewAccessChecker(cfg.GitHub.BaseURL, cfg.GitHub.AuthRepo, hc),
		HTTPClient: hc,
	})
	if err != nil {
		return fmt.Errorf("web server: %w", err)
	}

	return serve(ctx, log, srv.Handler())
}

// githubConfig derives the GitHub adapter's configuration from one setting, GITHUB_BASE_URL, which
// names an API root such as the fake sources server.
//
// BaseURL is the GraphQL *endpoint* the issue fetcher posts to. An empty setting must leave it
// empty so the adapter falls back to its own real GitHub default — concatenating "/graphql" onto
// "" would point production traffic at a relative path.
func githubConfig(cfg config.Config) github.Config {
	gh := github.Config{
		Token:       cfg.Secrets.GitHubToken,
		RESTBaseURL: cfg.GitHub.BaseURL,
		Repos:       cfg.GitHub.Repos,
	}
	if cfg.GitHub.BaseURL != "" {
		gh.BaseURL = cfg.GitHub.BaseURL + "/graphql"
	}
	return gh
}

// serve runs the HTTP server until ctx is cancelled, then shuts it down gracefully.
func serve(ctx context.Context, log *slog.Logger, h http.Handler) error {
	addr := ":" + envOr("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
