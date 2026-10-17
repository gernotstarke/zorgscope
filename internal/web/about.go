package web

import "net/http"

// handleAbout renders the about page (FR-1.18): the zorgscope logo drawn by a radar, the tagline
// and the credit.
//
// It is public, because the footer link that leads here is on the sign-in page too, and the page
// reads nothing of this application's: no snapshot, no configuration, no session data beyond the
// top bar's. A signed-in visitor gets that top bar — the switch, search, their login, the settings
// panel — so the page is not a dead end; the open counts are left out, since nothing is fetched for
// this page. A signed-in rendering names the login, so it is not stored by the browser, like every
// other signed-in page.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "About"}
	if sess, ok := s.session(r); ok {
		r = withSession(r, sess)
		w.Header().Set("Cache-Control", "no-store")
		data.Chrome = &chromeView{View: "about", Return: r.URL.RequestURI()}
	}
	s.execute(w, r, http.StatusOK, "about.html", data)
}
