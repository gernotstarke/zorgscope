// Command migrate applies the database schema and exits (FR-9.3 AC2).
//
// zorgscope migrates on every start-up, so this command is not needed to run the server. It exists
// for the operator: to prepare a freshly created Turso database before the first deploy, and to
// answer "did the schema apply cleanly?" without reading a Machine's logs. Migrate is idempotent,
// so running it against an up-to-date database is a no-op.
//
// Configuration comes from the environment, exactly as it does for the server: TURSO_URL and
// TURSO_AUTH_TOKEN. Nothing else is read, because nothing else is needed — this command never
// contacts an upstream source.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/libsql"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/web"
)

// migrateTimeout bounds the whole run. Applying the schema is a handful of statements against a
// database that is usually empty; anything slower than this is a connectivity problem, and an
// operator waiting at a terminal deserves to be told that rather than left hanging.
const migrateTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrate: schema is up to date")
}

func run() error {
	secrets := config.Secrets{
		TursoURL:       os.Getenv("TURSO_URL"),
		TursoAuthToken: os.Getenv("TURSO_AUTH_TOKEN"),
	}
	if secrets.TursoURL == "" {
		return errors.New("TURSO_URL is not set")
	}

	// Every error below is redacted before it is printed. The DSN carries the auth token, and the
	// driver quotes the DSN when a connection fails (QS-4.3) — so an error is rebuilt from a
	// redacted string rather than wrapped, because wrapping would carry the token to whatever
	// prints it next. This mirrors openStore in cmd/zorgscope.
	store, err := libsql.Open(secrets.TursoURL, secrets.TursoAuthToken)
	if err != nil {
		return errors.New("opening the database: " + web.Redact(secrets, err.Error()))
	}
	defer func() { _ = store.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		return errors.New("migrating the database: " + web.Redact(secrets, err.Error()))
	}
	return nil
}
