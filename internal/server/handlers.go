package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

var tileTemplates = map[string]string{"header": "tile_header", "attention": "tile_attention", "repos": "tile_repos"}

func (s *Server) data(r *http.Request, v app.View) pageData {
	return pageData{View: v, CSRF: csrfToken(r), PollSeconds: s.deps.Cfg.UI.TilePollSeconds}
}

func (s *Server) buildView(w http.ResponseWriter, r *http.Request) (app.View, bool) {
	v, err := s.deps.Dashboard.Build(r.Context())
	if err != nil {
		s.deps.Log.Error("dashboard build failed", "component", componentServer, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return app.View{}, false
	}
	return v, true
}

// handlePage renders the full dashboard (FR-1.x).
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "layout", s.data(r, v))
}

// handleTile renders one tile fragment for htmx polling (FR-2.7).
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	name, ok := tileTemplates[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, name, s.data(r, v))
}

// handleDismiss records a single dismissal and returns the refreshed attention tile (FR-2.5).
func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := domain.ParseItemID(r.PostForm.Get("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	upd, err := strconv.ParseInt(r.PostForm.Get("updated_at"), 10, 64)
	if err != nil {
		http.Error(w, "bad updated_at", http.StatusBadRequest)
		return
	}
	if err := app.Dismiss(r.Context(), s.deps.Store, s.deps.Clock, id, time.Unix(upd, 0).UTC()); err != nil {
		s.deps.Log.Error("dismiss failed", "component", componentServer, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.handleTileNamed(w, r, "tile_attention")
}

// handleDismissAll dismisses every current attention item (FR-2.5).
func (s *Server) handleDismissAll(w http.ResponseWriter, r *http.Request) {
	evs, err := s.deps.Dashboard.EvaluateAll(r.Context())
	if err == nil {
		err = app.DismissAll(r.Context(), s.deps.Store, s.deps.Clock, domain.FilterAttention(evs))
	}
	if err != nil {
		s.deps.Log.Error("dismiss all failed", "component", componentServer, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.handleTileNamed(w, r, "tile_attention")
}

// isHXRequest reports whether r was issued by htmx (which sets this header on every request it
// makes). A plain <form> POST from a browser with JS disabled never sets it (§8.5).
func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") != ""
}

// handleTileNamed renders tmpl as an htmx fragment for hx-triggered requests. For a no-JS browser
// POST (no HX-Request header) it instead redirects to "/" (303 See Other): the effect (dismissal)
// is already stored by the time this is called, but rendering the bare fragment here would leave
// the browser stranded on an unstyled, doctype-less partial with no way back except the Back
// button (§8.5 "everything works without JS except tile auto-refresh and passkeys").
func (s *Server) handleTileNamed(w http.ResponseWriter, r *http.Request, tmpl string) {
	if !isHXRequest(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, tmpl, s.data(r, v))
}

// handleRefresh triggers a fetch of all sources and returns immediately (FR-9.4). The htmx path
// (HX-Request set) keeps its existing 202-plus-plain-text contract, which the e2e suite exercises.
// A no-JS browser POST redirects to "/" (303 See Other) instead of landing on a bare text/plain
// response with no navigation back to the dashboard (§8.5).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	n := s.deps.Refresher.TriggerAll()
	if !isHXRequest(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("refreshing " + strconv.Itoa(n) + " sources"))
}

// handleStatus reports per-source fetch health as JSON (arc42 §8.8).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.deps.Store.Statuses(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type entry struct {
		SourceID    string `json:"source_id"`
		Kind        string `json:"kind"`
		LastSuccess string `json:"last_success,omitempty"`
		LastError   string `json:"last_error,omitempty"`
		Error       string `json:"error,omitempty"`
		NextRun     string `json:"next_run,omitempty"`
		Items       int    `json:"items"`
		InFlight    bool   `json:"in_flight"`
		AuthFailed  bool   `json:"auth_failed"`
	}
	fmtT := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	out := make([]entry, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, entry{SourceID: st.SourceID, Kind: st.Kind, LastSuccess: fmtT(st.LastSuccess), LastError: fmtT(st.LastError),
			Error: st.ErrorMsg, NextRun: fmtT(st.NextRun), Items: st.ItemCount, InFlight: st.InFlight, AuthFailed: st.AuthFailed})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sources": out, "in_flight": s.deps.Refresher.InFlight()})
}
