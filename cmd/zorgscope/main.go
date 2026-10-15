// Command zorgscope serves the personal status dashboard.
//
// The process is stateless: the only thing it remembers between requests is the last fetched item
// list, held in memory by internal/snapshot and refetched when it is stale. There is no database,
// no background scheduler and no refresh pipeline — the Fly Machine it runs on is stopped whenever
// nothing is in flight, and the first page view after a cold start starts the one fetch and shows
// the wait page until it lands (ADR-0011).
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
// that a hanging upstream fails a fetch rather than holding a fetch open for the whole of the
// cache's budget.
const upstreamTimeout = 20 * time.Second

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

	cache := snapshot.New(loggingSource(fetcher, log, cfg.Secrets), cfg.GitHub.CacheTTL, ports.SystemClock{})

	// One adapter answers every sign-in question — the owner's repository, the members' one, who
	// the visitor is — and revokes a member's token on sign-out (FR-8.3, FR-8.4).
	access := github.NewAccess(cfg.GitHub.BaseURL, cfg.Secrets.OAuthClientID, cfg.Secrets.OAuthClientSecret, hc)

	srv, err := web.New(web.Options{
		Config:     cfg,
		Cache:      cache,
		Clock:      ports.SystemClock{},
		Log:        log,
		Access:     access,
		Identity:   access,
		Revoker:    access,
		Repos:      access,
		HTTPClient: hc,
		// A member's view is fetched with their own token over their own repositories, none of
		// them asked for Dependabot alerts (spec 2026-10-12 §8): at most 24 requests, on the
		// member's own rate limit, never on GITHUB_TOKEN's.
		// Never the per-owner tokens of ADR-0017: those read private repositories and are the
		// owner's alone; a member's fetch carries the member's token and nothing else.
		MemberSource: func(token string, repos []string) ports.Source {
			c := githubConfig(cfg)
			c.Token, c.Repos, c.NoAlerts, c.OwnerTokens = token, repos, repos, nil
			return github.NewIssueFetcher(c, hc)
		},
	})
	if err != nil {
		return fmt.Errorf("web server: %w", err)
	}

	return serve(ctx, log, srv.Handler())
}

// loggingSource wraps src so that every failed fetch is logged once, at Warn, with how many items
// came back alongside the error — a partial failure and a total one read differently.
//
// The page shows a failure only to whoever is looking at the time, and the cache keeps the previous
// list, so without this line an upstream that fails for hours leaves no trace for an operator. It
// lives here rather than in internal/snapshot because only main holds both the logger and the
// secrets, and the error text is scrubbed of the latter before it is written (QS-4.3).
func loggingSource(src ports.Source, log *slog.Logger, secrets config.Secrets) ports.Source {
	return ports.SourceFunc(func(ctx context.Context) (domain.Fetched, error) {
		got, err := src.Fetch(ctx)
		if err != nil {
			log.Warn("fetch failed", "err", web.Redact(secrets, err.Error()), "items", len(got.Items))
		}
		return got, err
	})
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
		OwnerTokens: cfg.Secrets.OwnerTokens,
		Repos:       cfg.GitHub.Repos,
		NoAlerts:    cfg.GitHub.ReposWithoutAlerts(),
	}
	if cfg.GitHub.BaseURL != "" {
		gh.BaseURL = cfg.GitHub.BaseURL + "/graphql"
		gh.RESTBaseURL = cfg.GitHub.BaseURL
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
