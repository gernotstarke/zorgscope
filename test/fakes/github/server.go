// Package githubfake is a deterministic in-memory imitation of the parts of the GitHub API zorgscope
// uses (GraphQL issues/PRs, REST workflow runs and notifications). It serves adapter tests, e2e tests
// and the local demo (ADR-0010).
package githubfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Comment is a comment or review by Author at At.
type Comment struct {
	Author string
	At     time.Time
}

// Issue is an issue or (IsPR) pull request.
type Issue struct {
	Number         int
	Title          string
	URL            string
	Author         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Labels         []string
	Comments       []Comment
	IsPR           bool
	Draft          bool
	ReviewDecision string
	Reviews        []Comment
}

// Run is a workflow run.
type Run struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	Branch     string
	URL        string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Notification is a notification thread.
type Notification struct {
	ID           string
	Reason       string
	SubjectTitle string
	SubjectURL   string
	SubjectType  string
	Repo         string
	UpdatedAt    time.Time
	Unread       bool
}

// Repo is a repository with its open issues/PRs and runs.
type Repo struct {
	Owner, Name   string
	DefaultBranch string
	Issues        []Issue
	Runs          []Run
}

// Server holds the fake state. Safe for concurrent use.
type Server struct {
	mu            sync.Mutex
	repos         map[string]*Repo
	notifications []Notification
	tokenExpiry   *time.Time
	failStatus    int
	failRateLimit bool
	requests      int
	seedNow       time.Time
}

// New returns an empty server.
func New() *Server { return &Server{repos: map[string]*Repo{}} }

// Reset clears all state and reseeds it with the standard Seed data (relative to the wall clock, or
// to the last time Seed/reset last ran with an explicit now, if any). This is deliberately not "back
// to empty": the e2e suite calls /__control/reset between scenarios and needs a known-good starting
// state to assert against, not a blank server that every scenario would have to re-populate itself.
func (s *Server) Reset() {
	s.mu.Lock()
	now := s.seedNow
	s.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	s.repos = map[string]*Repo{}
	s.notifications = nil
	s.tokenExpiry = nil
	s.failStatus = 0
	s.mu.Unlock()
	Seed(s, now)
}

// AddRepo adds or replaces a repo.
func (s *Server) AddRepo(r Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	for k := range r.Issues {
		if r.Issues[k].URL == "" {
			r.Issues[k].URL = issueURL(r.Owner+"/"+r.Name, r.Issues[k])
		}
	}
	rr := r
	s.repos[r.Owner+"/"+r.Name] = &rr
}

func issueURL(repo string, i Issue) string {
	kind := "issues"
	if i.IsPR {
		kind = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", repo, kind, i.Number)
}

// AddIssue upserts an issue by number; the repo is created if missing.
func (s *Server) AddIssue(repo string, i Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[repo]
	if r == nil {
		owner, name, _ := strings.Cut(repo, "/")
		r = &Repo{Owner: owner, Name: name, DefaultBranch: "main"}
		s.repos[repo] = r
	}
	if i.URL == "" {
		i.URL = issueURL(repo, i)
	}
	for k := range r.Issues {
		if r.Issues[k].Number == i.Number {
			r.Issues[k] = i
			return
		}
	}
	r.Issues = append(r.Issues, i)
}

// SetRuns replaces the runs of a repo.
func (s *Server) SetRuns(repo string, runs []Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.repos[repo]; r != nil {
		r.Runs = runs
	}
}

// SetNotifications replaces notifications.
func (s *Server) SetNotifications(n []Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = n
}

// SetTokenExpiry makes every response carry the token expiration header.
func (s *Server) SetTokenExpiry(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenExpiry = &t
}

// FailNext makes the next API request fail with status (rate-limit headers when rateLimited).
func (s *Server) FailNext(status int, rateLimited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStatus, s.failRateLimit = status, rateLimited
}

// Requests counts API requests served (control endpoints excluded).
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", s.api(s.graphql))
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", s.api(s.runs))
	mux.HandleFunc("GET /notifications", s.api(s.notificationsHandler))
	mux.HandleFunc("POST /__control/issues", s.controlIssues)
	mux.HandleFunc("POST /__control/runs", s.controlRuns)
	mux.HandleFunc("POST /__control/fail", s.controlFail)
	mux.HandleFunc("POST /__control/reset", s.controlReset)
	return mux
}

// api wraps API handlers with auth, forced failures, counting and common headers.
func (s *Server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		fail, rl := s.failStatus, s.failRateLimit
		s.failStatus = 0
		exp := s.tokenExpiry
		now := s.seedNow
		s.mu.Unlock()
		if now.IsZero() {
			now = time.Now()
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-RateLimit-Limit", "5000")
		if exp != nil {
			w.Header().Set("GitHub-Authentication-Token-Expiration", exp.UTC().Format("2006-01-02 15:04:05 MST"))
		}
		if fail != 0 {
			if rl {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(15*time.Minute).Unix(), 10))
			}
			w.WriteHeader(fail)
			_, _ = fmt.Fprintf(w, `{"message":"forced failure %d"}`, fail)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(strings.ToLower(auth), "bearer ") || len(auth) <= 7 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		h(w, r)
	}
}

func actor(login string) any {
	if login == "" {
		return nil
	}
	return map[string]any{"login": login}
}

func (s *Server) issueNode(i Issue) map[string]any {
	labels := make([]any, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	node := map[string]any{
		"number": i.Number, "title": i.Title, "url": i.URL, "createdAt": i.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": i.UpdatedAt.UTC().Format(time.RFC3339), "author": actor(i.Author),
		"labels":   map[string]any{"nodes": labels},
		"comments": map[string]any{"totalCount": len(i.Comments), "nodes": lastComment(i.Comments, "createdAt")},
	}
	if i.IsPR {
		node["isDraft"] = i.Draft
		node["reviewDecision"] = i.ReviewDecision
		node["reviews"] = map[string]any{"nodes": lastComment(i.Reviews, "submittedAt")}
	}
	return node
}

func lastComment(cs []Comment, timeKey string) []any {
	if len(cs) == 0 {
		return []any{}
	}
	c := cs[len(cs)-1]
	return []any{map[string]any{"author": actor(c.Author), timeKey: c.At.UTC().Format(time.RFC3339)}}
}

func (s *Server) page(list []Issue, after any, n int) map[string]any {
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	start := 0
	if c, ok := after.(string); ok && c != "" {
		start, _ = strconv.Atoi(c)
	}
	if start < 0 {
		start = 0
	}
	if start > len(list) {
		start = len(list)
	}
	if n <= 0 {
		n = 100
	}
	end := start + n
	if end > len(list) {
		end = len(list)
	}
	nodes := make([]any, 0)
	for _, i := range list[start:end] {
		nodes = append(nodes, s.issueNode(i))
	}
	return map[string]any{
		"pageInfo": map[string]any{"hasNextPage": end < len(list), "endCursor": strconv.Itoa(end)},
		"nodes":    nodes,
	}
}

func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
		return
	}
	v := req.Variables
	owner, _ := v["owner"].(string)
	name, _ := v["name"].(string)
	n := 100
	if f, ok := v["n"].(float64); ok {
		n = int(f)
	}
	withIssues, _ := v["withIssues"].(bool)
	withPRs, _ := v["withPRs"].(bool)

	s.mu.Lock()
	repo := s.repos[owner+"/"+name]
	var issues, prs []Issue
	var defaultBranch string
	if repo != nil {
		defaultBranch = repo.DefaultBranch
		for _, i := range repo.Issues {
			if i.IsPR {
				prs = append(prs, i)
			} else {
				issues = append(issues, i)
			}
		}
	}
	now := s.seedNow
	s.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}

	data := map[string]any{"rateLimit": map[string]any{"cost": 1, "remaining": 4999, "resetAt": now.Add(time.Hour).UTC().Format(time.RFC3339)}}
	if repo == nil {
		data["repository"] = nil
	} else {
		rm := map[string]any{"nameWithOwner": owner + "/" + name, "defaultBranchRef": map[string]any{"name": defaultBranch}}
		if withIssues {
			rm["issues"] = s.page(issues, v["issuesAfter"], n)
		}
		if withPRs {
			rm["pullRequests"] = s.page(prs, v["prsAfter"], n)
		}
		data["repository"] = rm
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repo := s.repos[r.PathValue("owner")+"/"+r.PathValue("repo")]
	var runs []Run
	if repo != nil {
		runs = append(runs, repo.Runs...)
	}
	s.mu.Unlock()
	if repo == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	// GitHub returns workflow runs newest first; monotonically increasing run IDs make this fake's
	// ordering deterministic without relying on identical test timestamps.
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	branch := r.URL.Query().Get("branch")
	list := make([]any, 0)
	for _, run := range runs {
		if branch != "" && run.Branch != branch {
			continue
		}
		list = append(list, map[string]any{"id": run.ID, "name": run.Name, "html_url": run.URL, "status": run.Status,
			"conclusion": run.Conclusion, "head_branch": run.Branch, "created_at": run.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at": run.UpdatedAt.UTC().Format(time.RFC3339), "run_started_at": run.CreatedAt.UTC().Format(time.RFC3339)})
	}
	if per := r.URL.Query().Get("per_page"); per != "" {
		if p, err := strconv.Atoi(per); err == nil && p >= 0 && p < len(list) {
			list = list[:p]
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "workflow_runs": list})
}

func (s *Server) notificationsHandler(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	ns := append([]Notification(nil), s.notifications...)
	s.mu.Unlock()
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].ID < ns[j].ID })
	list := make([]any, 0, len(ns))
	for _, n := range ns {
		list = append(list, map[string]any{"id": n.ID, "reason": n.Reason, "unread": n.Unread,
			"updated_at": n.UpdatedAt.UTC().Format(time.RFC3339),
			"subject":    map[string]any{"title": n.SubjectTitle, "url": n.SubjectURL, "type": n.SubjectType},
			"repository": map[string]any{"full_name": n.Repo}})
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) controlIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  string `json:"repo"`
		Issue Issue  `json:"issue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.AddIssue(req.Repo, req.Issue)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) controlRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo string `json:"repo"`
		Runs []Run  `json:"runs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.SetRuns(req.Repo, req.Runs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) controlFail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status      int  `json:"status"`
		RateLimited bool `json:"rate_limited"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.FailNext(req.Status, req.RateLimited)
	w.WriteHeader(http.StatusNoContent)
}

// controlReset handles POST /__control/reset: it resets the server, then reseeds it via Reset (which
// calls Seed). See the Reset doc comment for why "reseed", not "empty".
func (s *Server) controlReset(w http.ResponseWriter, _ *http.Request) {
	s.Reset()
	w.WriteHeader(http.StatusNoContent)
}
