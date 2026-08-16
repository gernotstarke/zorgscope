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

func (s *Server) handleTileNamed(w http.ResponseWriter, r *http.Request, tmpl string) {
	v, ok := s.buildView(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, tmpl, s.data(r, v))
}

// handleRefresh triggers a fetch of all sources and returns immediately (FR-9.4).
func (s *Server) handleRefresh(w http.ResponseWriter, _ *http.Request) {
	n := s.deps.Refresher.TriggerAll()
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
