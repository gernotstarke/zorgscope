// Command zorgscope runs the dashboard server.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/gernotstarke/zorgscope/internal/server"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", server.HealthHandler())
	mux.Handle("GET /readyz", server.ReadyHandler(func() bool { return true }))
	slog.Info("zorgscope listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, mux); err != nil { //nolint:gosec // timeouts configured in Task 15
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
