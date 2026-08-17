package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// FR-9.4 AC2: liveness must answer without authentication and without touching upstreams.
func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	newMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestLogLevelFallsBackToInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "not-a-level")
	if got := logLevel().String(); got != "INFO" {
		t.Errorf("logLevel() = %s, want INFO", got)
	}
}
