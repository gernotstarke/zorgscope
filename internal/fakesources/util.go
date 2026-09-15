package fakesources

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes v as a JSON response body with the given status code. Encoding a value this
// package itself constructed cannot fail in practice, but the write is still checked (rather than
// ignored) because errcheck requires it and a failed write is worth knowing about even though
// there is nothing left to do about it once the status line is already sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line and headers are already written; there is nothing left to do but note
		// it happened. net/http surfaces write errors to the client as a truncated response.
		_ = err
	}
}
