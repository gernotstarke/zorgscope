package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

const maxJSONBody = 1 << 20 // configuration is small; cap all API bodies at 1 MiB

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body is not valid for this endpoint")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func (s *Server) handleAPIDashboard(w http.ResponseWriter, r *http.Request) {
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	body, err := json.Marshal(v)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "encode_failed", "dashboard could not be encoded")
		return
	}
	etag := dashboardETag(v)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}

// dashboardETag is weak because display-only clock strings can differ while the product state is
// equivalent. Time-dependent attention levels, age buckets and expiry-day counts remain in the
// hash, so a threshold crossing still invalidates clients; only continuously changing labels are
// ignored. This makes polling useful without freezing meaningful time-based state.
func dashboardETag(v app.View) string {
	stable := v
	stable.GeneratedAt = time.Time{}
	stable.Header.Date = ""
	stable.Header.Time = ""
	stable.Header.Sources = append([]app.SourceStatusView(nil), v.Header.Sources...)
	for i := range stable.Header.Sources {
		stable.Header.Sources[i].Age = ""
	}
	stable.Attention.Rows = append([]app.AttentionRow(nil), v.Attention.Rows...)
	for i := range stable.Attention.Rows {
		stable.Attention.Rows[i].Age = ""
	}
	stable.Repos = append([]app.RepoCard(nil), v.Repos...)
	for i := range stable.Repos {
		stable.Repos[i].Build.Age = ""
	}
	body, _ := json.Marshal(stable) // app.View contains only JSON-marshalable values
	hash := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(hash[:]) + `"`
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) { s.handleStatus(w, r) }

func (s *Server) handleAPIDismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		UpdatedAt int64  `json:"updated_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id, err := domain.ParseItemID(req.ID)
	if err != nil || req.UpdatedAt <= 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_dismissal", "id and updated_at must identify a current item")
		return
	}
	if err := app.Dismiss(r.Context(), s.deps.Store, s.deps.Clock, id, time.Unix(req.UpdatedAt, 0).UTC()); err != nil {
		s.deps.Log.Error("api dismiss failed", "component", componentServer, "err", err)
		writeAPIError(w, http.StatusInternalServerError, "dismiss_failed", "dismissal could not be stored")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIDismissAll(w http.ResponseWriter, r *http.Request) {
	evs, err := s.dashboard().EvaluateAll(r.Context())
	if err == nil {
		err = app.DismissAll(r.Context(), s.deps.Store, s.deps.Clock, domain.FilterAttention(evs))
	}
	if err != nil {
		s.deps.Log.Error("api dismiss all failed", "component", componentServer, "err", err)
		writeAPIError(w, http.StatusInternalServerError, "dismiss_all_failed", "dismissals could not be stored")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIRefresh(w http.ResponseWriter, _ *http.Request) {
	n := s.refresher().TriggerAll()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "refreshing", "sources": n})
}

// revisionFromRequest accepts the normal If-Match form and a query fallback convenient for
// clients that cannot attach a body to DELETE. Quotes around an HTTP ETag are stripped.
func revisionFromRequest(r *http.Request) string {
	rev := r.Header.Get("If-Match")
	if rev == "" {
		rev = r.URL.Query().Get("revision")
	}
	if len(rev) >= 2 && rev[0] == '"' && rev[len(rev)-1] == '"' {
		rev = rev[1 : len(rev)-1]
	}
	return rev
}

func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		writeAPIError(w, http.StatusNotImplemented, "config_unavailable", "runtime configuration is not enabled")
		return
	}
	if r.Method == http.MethodGet {
		doc := s.deps.Config.Document()
		w.Header().Set("ETag", `"`+doc.Revision+`"`)
		writeJSON(w, http.StatusOK, doc)
		return
	}
	revision := revisionFromRequest(r)
	if revision == "" {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "send the current revision in If-Match")
		return
	}
	var next config.EditableConfig
	if !decodeJSON(w, r, &next) {
		return
	}
	old, _ := s.deps.Config.Current()
	doc, err := s.deps.Config.UpdateAndApply(revision, next, func(candidate *config.Config) error {
		return s.activateManagedConfig(old, candidate)
	})
	if err != nil {
		s.writeConfigError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+doc.Revision+`"`)
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleAPIConfigSecret(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		writeAPIError(w, http.StatusNotImplemented, "config_unavailable", "runtime configuration is not enabled")
		return
	}
	revision := revisionFromRequest(r)
	if revision == "" {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "send the current revision in If-Match")
		return
	}
	name := config.SecretName(r.PathValue("name"))
	old, _ := s.deps.Config.Current()
	var (
		doc config.Document
		err error
	)
	if r.Method == http.MethodDelete {
		doc, err = s.deps.Config.ClearSecretAndApply(revision, name, func(candidate *config.Config) error {
			return s.activateManagedConfig(old, candidate)
		})
	} else {
		var req struct {
			Value string `json:"value"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		doc, err = s.deps.Config.SetSecretAndApply(revision, name, req.Value, func(candidate *config.Config) error {
			return s.activateManagedConfig(old, candidate)
		})
	}
	if err != nil {
		s.writeConfigError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+doc.Revision+`"`)
	writeJSON(w, http.StatusOK, doc)
}

// activateManagedConfig is passed into Manager's atomic mutation transaction. It must never call
// back into Manager: the Manager lock intentionally remains held until activation succeeds or the
// persisted files are rolled back.
func (s *Server) activateManagedConfig(previous, candidate *config.Config) error {
	if s.deps.Redactor != nil {
		// Keep both generations redacted while Apply drains in-flight requests using the previous
		// credentials. If Apply fails, the previous generation remains live and its set is restored.
		values := append(configSecretValues(previous), configSecretValues(candidate)...)
		s.deps.Redactor.Replace(values)
	}
	if s.deps.Runtime != nil {
		if err := s.deps.Runtime.Apply(candidate); err != nil {
			if s.deps.Redactor != nil {
				s.deps.Redactor.Replace(configSecretValues(previous))
			}
			return err
		}
	}
	if s.deps.Redactor != nil {
		s.deps.Redactor.Replace(configSecretValues(candidate))
	}
	return nil
}

func configSecretValues(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return []string{cfg.Secrets.GitHubToken, cfg.Secrets.PlausibleAPIKey, cfg.Secrets.SessionSecret,
		cfg.Secrets.EnrollToken, cfg.Secrets.APIToken, cfg.Secrets.ConfigKey}
}

func (s *Server) writeConfigError(w http.ResponseWriter, err error) {
	var validation *config.ValidationError
	var conflict *config.RevisionConflictError
	switch {
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "revision_conflict", "message": "configuration changed; reload before saving", "current_revision": conflict.Current})
	case errors.As(err, &validation):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_config", "field": validation.Key, "message": validation.Msg})
	case errors.Is(err, config.ErrUnknownSecret):
		writeAPIError(w, http.StatusNotFound, "unknown_secret", "that secret is deployment-managed or unknown")
	default:
		s.deps.Log.Error("config mutation failed", "component", componentServer, "err", err)
		writeAPIError(w, http.StatusInternalServerError, "config_update_failed", "configuration was not changed")
	}
}
