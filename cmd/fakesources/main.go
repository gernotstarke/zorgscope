// Command fakesources serves fake upstream APIs (GitHub in M1) for e2e tests and the local demo.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()
	gh := githubfake.New()
	githubfake.Seed(gh, time.Now())
	slog.Info("fakesources listening", "addr", *addr, "github", "/graphql, /repos/…/actions/runs, /notifications, /__control/*")
	srv := &http.Server{Addr: *addr, Handler: gh.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("fakesources stopped", "err", err)
		os.Exit(1)
	}
}
