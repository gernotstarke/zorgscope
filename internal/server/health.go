// Package server contains the HTTP delivery layer of zorgscope.
package server

import "net/http"

// HealthHandler answers liveness probes. It never touches data.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
}

// ReadyHandler answers readiness probes: 200 when ready() is true, else 503.
func ReadyHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
}
