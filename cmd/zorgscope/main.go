// Command zorgscope serves the personal status dashboard.
//
// The process is deliberately stateless and short-lived: the Fly Machine it runs on is stopped
// whenever nothing is in flight, so there is no background scheduler here. Upstream data is
// refreshed by an external cron service calling POST /api/refresh (ADR-0003).
//
// Everything is assembled in one place, run: configuration, the store, the enabled source
// fetchers, the refresh runner and the HTTP server. Nothing below it reads the environment.
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
	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/adapters/plausible"
	"github.com/gernotstarke/zorgscope/internal/adapters/todoist"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/refresh"
	"github.com/gernotstarke/zorgscope/internal/web"
)

// upstreamTimeout bounds one HTTP call to a source. It is shorter than the Machine's own patience
// so that a hanging upstream fails a refresh rather than holding the lease until it expires.
const upstreamTimeout = 20 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		// Every error reaching here has already been scrubbed of secrets where it could carry
		// one; see openStore (QS-4.3).
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

	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// config.Load has already validated the zone, and internal/config blank-imports time/tzdata
	// so the lookup works in the distroless image.
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}

	clock := ports.SystemClock{}
	hc := &http.Client{Timeout: upstreamTimeout}
	fetchers := buildFetchers(cfg, hc, clock, loc)
	for _, f := range fetchers {
		log.Info("source enabled", "source", f.Name())
	}

	// The notifier stays nil until Task 17; the runner treats that as "announce nothing".
	runner := refresh.New(store, fetchers, clock, nil, log)

	srv, err := web.New(web.Options{
		Config: cfg,
		Store:  store,
		Runner: runner,
		Clock:  clock,
		Log:    log,
	})
	if err != nil {
		return fmt.Errorf("web server: %w", err)
	}

	return serve(ctx, log, srv.Handler())
}

// openStore opens the libSQL store and brings its schema up to date.
//
// The error from the driver is scrubbed before it is returned: the DSN carries the Turso auth
// token, and a connection failure quotes the DSN (QS-4.3). That is also why the returned error is
// built from a redacted string rather than wrapping the original — a wrapped error would carry the
// token to whatever prints it next.
func openStore(ctx context.Context, cfg config.Config) (*libsql.Store, error) {
	store, err := libsql.Open(cfg.Secrets.TursoURL, cfg.Secrets.TursoAuthToken)
	if err != nil {
		return nil, errors.New("opening the database: " + web.Redact(cfg.Secrets, err.Error()))
	}

	migrateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := store.Migrate(migrateCtx); err != nil {
		_ = store.Close()
		return nil, errors.New("migrating the database: " + web.Redact(cfg.Secrets, err.Error()))
	}
	return store, nil
}

// buildFetchers builds one fetcher per enabled source (FR-8.2 AC2). A source whose credential is
// missing — or that has nothing configured to watch — is simply absent from the refresh, rather
// than present and failing every run.
func buildFetchers(cfg config.Config, hc *http.Client, clock ports.Clock, loc *time.Location) []ports.SourceFetcher {
	var fetchers []ports.SourceFetcher
	if cfg.Enabled("github") {
		gh := githubConfig(cfg)
		// Issues and builds are two fetchers over one credential: they use different GitHub APIs
		// and one failing must not hide the other's result.
		fetchers = append(fetchers, github.NewIssueFetcher(gh, hc), github.NewBuildFetcher(gh, hc))
	}
	if cfg.Enabled("plausible") {
		fetchers = append(fetchers, plausible.New(plausible.Config{
			APIKey:  cfg.Secrets.PlausibleKey,
			BaseURL: cfg.Plausible.BaseURL,
			Sites:   cfg.Plausible.Sites,
		}, hc))
	}
	if cfg.Enabled("todoist") {
		fetchers = append(fetchers, todoist.New(todoist.Config{
			Token:    cfg.Secrets.TodoistToken,
			BaseURL:  cfg.Todoist.BaseURL,
			Filter:   cfg.Todoist.Filter,
			Location: loc,
		}, hc, clock))
	}
	return fetchers
}

// githubConfig derives the GitHub adapter's configuration from one setting, GITHUB_BASE_URL, which
// names an API root such as the fake sources server.
//
// The two base URLs are not interchangeable: BaseURL is the GraphQL *endpoint* the issue fetcher
// posts to, RESTBaseURL is the REST *root* the build fetcher appends paths to. An empty setting
// must leave both empty so that each adapter falls back to its own real GitHub default —
// concatenating "/graphql" onto "" would point production traffic at a relative path.
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
