package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// defaultRESTBaseURL is used when Config.RESTBaseURL is empty.
const defaultRESTBaseURL = "https://api.github.com"

// githubAPIVersion is sent as X-GitHub-Api-Version on every REST request, per GitHub's REST API
// versioning scheme.
const githubAPIVersion = "2022-11-28"

// BuildFetcher fetches GitHub Actions build/workflow status for the repositories in Config, over
// the GitHub REST API (FR-2.3).
type BuildFetcher struct {
	hc           *http.Client
	token        string
	restBaseURL  string
	badgeBaseURL string
	repos        []string
}

// NewBuildFetcher builds a BuildFetcher from cfg. hc supplies the transport and any test-only
// settings (as in this package's tests, which pass an httptest server's client). Unlike
// IssueFetcher, the token is not composed into hc's transport here — it is added per-request as
// an Authorization header, since the REST API needs no other client-side machinery. When
// cfg.RESTBaseURL is non-empty, requests go to that URL (used to point at a fake or an
// Enterprise instance); otherwise they go to GitHub's public REST API root.
func NewBuildFetcher(cfg Config, hc *http.Client) *BuildFetcher {
	base := cfg.RESTBaseURL
	if base == "" {
		base = defaultRESTBaseURL
	}
	badgeBase := cfg.BadgeBaseURL
	if badgeBase == "" {
		badgeBase = defaultBadgeBaseURL
	}
	return &BuildFetcher{
		hc:           hc,
		token:        cfg.Token,
		restBaseURL:  base,
		badgeBaseURL: strings.TrimSuffix(badgeBase, "/"),
		repos:        cfg.Repos,
	}
}

// Name identifies this fetcher's source (FR-2.3).
func (f *BuildFetcher) Name() string { return "github-builds" }

// Fetch retrieves the latest build status for every repository in f.repos. A failure fetching
// one repository does not lose builds already fetched from the others (QS-1.4): every
// per-repository error is collected, joined with errors.Join, and returned alongside every build
// successfully fetched — never returned early on the first failure.
func (f *BuildFetcher) Fetch(ctx context.Context) (ports.FetchResult, error) {
	var builds []domain.Build
	var errs []error

	for _, repo := range f.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			errs = append(errs, fmt.Errorf("github: %q is not owner/name", repo))
			continue
		}

		b, err := f.fetchLatestBuild(ctx, owner, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("github: fetching build status for %s: %w", repo, err))
			continue
		}
		if b != nil {
			builds = append(builds, *b)
		}
	}

	f.attachBadges(ctx, builds)

	// OwnsBuilds: this fetcher is the one that fills the builds table, so what it returns is the
	// whole truth about it and an empty result means every watched repository has lost its build
	// — a repository dropped from the configuration, or a fleet whose CI is gone. The runner
	// stores it either way and the store's complement delete clears what is no longer there
	// (FR-2.3). A partial failure never reaches that path: errs is non-nil, and the runner stores
	// nothing from a result carrying an error.
	return ports.FetchResult{Builds: builds, OwnsBuilds: true}, errors.Join(errs...)
}

// workflowRun is one entry of the GitHub Actions "list workflow runs" REST response. Only the
// fields this fetcher needs are declared; the real response has many more.
type workflowRun struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	Status       string  `json:"status"`
	Conclusion   *string `json:"conclusion"`
	HTMLURL      string  `json:"html_url"`
	RunStartedAt string  `json:"run_started_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// runsResponse is the REST "list workflow runs" envelope.
type runsResponse struct {
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

// fetchLatestBuild fetches owner/name's workflow runs and reduces them to at most one
// domain.Build per FR-2.3 AC2/AC3. It returns (nil, nil) when the repository has no runs at all
// (AC3: not a failure) — but returns an error, not (nil, nil), when the response has runs that
// could not be interpreted (e.g. every run_started_at fails to parse): only a genuinely empty
// workflow_runs array is the legitimate "no CI configured" case; a non-empty array we could not
// read is a failure, symmetric with the non-200 handling below.
//
// The branch query parameter is deliberately omitted (only per_page=10 is sent). The brief's
// {branch}/{per_page} shape assumes a repository's default branch is known, but nothing in this
// system fetches or configures it, and guessing a common name like "main" would silently filter
// out every run for any repository using something else — turning a genuinely broken build into
// an empty, healthy-looking tile. Fetching the most recent runs across every branch is the
// honest failure mode: it may occasionally surface a feature-branch run instead of main's, but
// it never manufactures false-green silence.
func (f *BuildFetcher) fetchLatestBuild(ctx context.Context, owner, name string) (*domain.Build, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs?per_page=10", f.restBaseURL, owner, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)

	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// A non-200 must not be decoded: an error body (e.g. a 500 with an HTML page) could
		// decode into a zero-value runsResponse with an empty WorkflowRuns, which would be
		// indistinguishable from AC3's legitimate "no CI configured" case and silently hide a
		// broken tile.
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body runsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	if len(body.WorkflowRuns) == 0 {
		return nil, nil // AC3: no workflows is not an error.
	}

	newest, newestCompleted := reduceRuns(body.WorkflowRuns)
	if newest == nil {
		// workflow_runs was non-empty (checked above) but not one entry had a run_started_at
		// that parsed as RFC 3339. This is not AC3's legitimate "no CI configured" case — the
		// upstream sent runs, just in a shape we could not read — so, symmetrically with the
		// non-200 check above, it must surface as an error rather than silently reading as an
		// empty, healthy-looking tile.
		return nil, fmt.Errorf("%d workflow run(s) returned but none had a usable run_started_at", len(body.WorkflowRuns))
	}

	// FetchedAt is deliberately left zero: Store.UpsertBuilds stamps every row with the run's
	// `now` and never reads the value carried here, so anything set would be overwritten before it
	// could be seen. Filling it in would mean calling time.Now outside ports.SystemClock — the one
	// place in this system allowed to read the wall clock — to produce a value nothing uses. Same
	// convention as Item.FirstSeenAt: the store owns the timestamps it writes.
	b := &domain.Build{
		Repo: owner + "/" + name,
		// The display name and the file are both kept: the name is what the details page shows,
		// and the path is the only one of the two that can address the workflow from outside —
		// a badge takes the file name (FR-2.3 AC5). Both come from the newest run rather than the
		// newest completed one, for the same reason Status does: they describe the run GitHub is
		// reporting on now.
		Workflow:     newest.Name,
		WorkflowPath: newest.Path,
		Status:       newest.Status,
		RunURL:       newest.HTMLURL,
	}
	if newestCompleted != nil {
		if newestCompleted.Conclusion != nil {
			b.Conclusion = *newestCompleted.Conclusion
		}
		// newestCompleted was selected because its run_started_at parsed (reduceRuns requires
		// that); UpdatedAt is a second, independent timestamp on the same run and can fail to
		// parse on its own. That is deliberately not promoted to a repository-level error the
		// way an all-runs-unparseable response is above: the run itself was readable and its
		// Conclusion is FR-2.3 AC2's substantive field, so a malformed FinishedAt only costs a
		// display detail — it doesn't call the whole build result into question. FinishedAt is
		// left zero in that case rather than fabricated.
		if t, err := time.Parse(time.RFC3339, newestCompleted.UpdatedAt); err == nil {
			b.FinishedAt = t.UTC()
		}
	}

	return b, nil
}

// reduceRuns scans runs (not assuming any particular array order — the REST API documents newest
// first, but nothing here relies on that) and returns the run with the newest run_started_at
// (newest), plus the newest run among those whose status is "completed" (newestCompleted, nil if
// none). run_started_at, not updated_at, is used to rank "newest run overall" because it reflects
// when GitHub actually started the run — a still-running run's updated_at ticks forward every
// time a job progresses, which is not what "newest" should mean when comparing it against an
// older, already-finished run.
func reduceRuns(runs []workflowRun) (newest, newestCompleted *workflowRun) {
	var newestAt, newestCompletedAt time.Time

	for i := range runs {
		r := &runs[i]
		started, err := time.Parse(time.RFC3339, r.RunStartedAt)
		if err != nil {
			continue
		}
		if newest == nil || started.After(newestAt) {
			newest = r
			newestAt = started
		}
		if r.Status == "completed" && (newestCompleted == nil || started.After(newestCompletedAt)) {
			newestCompleted = r
			newestCompletedAt = started
		}
	}

	return newest, newestCompleted
}

// ---------------------------------------------------------------- badges

// defaultBadgeBaseURL is where a workflow's badge image comes from when Config does not say
// (FR-2.3 AC5): shields.io's GitHub Actions workflow-status endpoint, which takes owner, repository
// and workflow *file* as its last three path segments.
const defaultBadgeBaseURL = "https://img.shields.io/github/actions/workflow/status"

// badgeTimeout bounds one badge request, and is deliberately far shorter than upstreamTimeout.
//
// A badge is a picture of something this run already knows. It is worth a moment and not a second
// more: the run's own budget (QS-2.5) belongs to the data, and a badge service having a slow
// afternoon must not be able to spend it. Everything below is built so that the worst a badge can
// cost a refresh is this, once, in parallel.
const badgeTimeout = 3 * time.Second

// maxBadgeBytes caps what is read from a badge response. A badge is around 1.5 kB of SVG; this is
// room for a much larger one and a hard stop well before anything that would be stored in a
// database row and rendered into a page.
const maxBadgeBytes = 64 << 10

// attachBadges fills in each build's Badge, in place.
//
// Every badge is fetched at once rather than one after another, because they are independent and
// eight of them in series would be eight timeouts in the worst case instead of one.
//
// Nothing here can fail the fetch it belongs to. A badge that does not arrive — the service is
// slow, is rate limiting, has been blocked, has never heard of the workflow — leaves Badge empty,
// and an empty badge is a row that says so. That is the same rule a failed announcement follows
// (FR-6.1 AC3): a decorative step must never take the substantive one down with it, and this one
// is not even reported, because there is nothing an operator would do about it.
func (f *BuildFetcher) attachBadges(ctx context.Context, builds []domain.Build) {
	var wg sync.WaitGroup
	for i := range builds {
		workflow := builds[i].BadgeWorkflow()
		if workflow == "" {
			continue
		}
		wg.Add(1)
		go func(b *domain.Build, workflow string) {
			defer wg.Done()
			if svg, err := f.fetchBadge(ctx, b.Repo, workflow); err == nil {
				b.Badge = svg
			}
		}(&builds[i], workflow)
	}
	wg.Wait()
}

// fetchBadge retrieves one workflow's badge image.
//
// The branch is deliberately absent from the query. Without it the badge reports the most recent
// run of that workflow on any branch, which is the rule fetchLatestBuild uses to pick the run it
// sits beside — pinning the badge to a branch would produce a badge that contradicts its own row.
//
// No credential is sent. The badge service is not GitHub and has no business holding this
// installation's token, and every repository it can answer for is one it can already see
// (QS-4.3).
func (f *BuildFetcher) fetchBadge(ctx context.Context, repo, workflow string) ([]byte, error) {
	owner, name, ok := splitRepo(repo)
	if !ok {
		return nil, fmt.Errorf("github: %q is not owner/name", repo)
	}

	ctx, cancel := context.WithTimeout(ctx, badgeTimeout)
	defer cancel()

	badgeURL := f.badgeBaseURL + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" +
		url.PathEscape(workflow)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, badgeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/svg+xml")

	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	// The content type is checked rather than assumed: a rate-limit page or a captive portal
	// answering 200 with HTML would otherwise be stored as this repository's badge and handed to
	// a browser as an image.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		return nil, fmt.Errorf("unexpected content type %q", ct)
	}

	svg, err := io.ReadAll(io.LimitReader(resp.Body, maxBadgeBytes))
	if err != nil {
		return nil, err
	}
	if len(svg) == 0 {
		return nil, errors.New("empty badge")
	}
	return svg, nil
}
