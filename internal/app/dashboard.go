package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Dashboard assembles the View from stores — never from upstream (arc42 §6.2, QG-2).
type Dashboard struct {
	store        ports.Store
	clock        ports.Clock
	cfg          *config.Config
	activeSource map[string]struct{}
}

// NewDashboard creates a dashboard query service.
func NewDashboard(store ports.Store, clock ports.Clock, cfg *config.Config) *Dashboard {
	return &Dashboard{store: store, clock: clock, cfg: cfg}
}

// NewDashboardForSources creates a dashboard that ignores cached rows left by sources removed by
// a live configuration update. SQLite deliberately keeps those rows for now, but they must not
// leak back into attention or source-health views after the owning source is disabled.
func NewDashboardForSources(store ports.Store, clock ports.Clock, cfg *config.Config, sourceIDs []string) *Dashboard {
	active := make(map[string]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		active[id] = struct{}{}
	}
	return &Dashboard{store: store, clock: clock, cfg: cfg, activeSource: active}
}

func (d *Dashboard) sourceActive(id string) bool {
	if d.activeSource == nil {
		return true
	}
	_, ok := d.activeSource[id]
	return ok
}

// EvaluateAll loads all items and evaluates each against its previous snapshot and dismissal.
// Sources in auth-failed state contribute a synthetic AUTH FAILED credential item (FR-11.3).
func (d *Dashboard) EvaluateAll(ctx context.Context) ([]domain.Evaluated, error) {
	now := d.clock.Now()
	rules := d.cfg.Rules()
	items, err := d.store.AllItems(ctx)
	if err != nil {
		return nil, err
	}
	dismissals, err := d.store.Dismissals(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := d.store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	filteredItems := items[:0]
	for _, item := range items {
		if d.sourceActive(item.ID.SourceID) {
			filteredItems = append(filteredItems, item)
		}
	}
	items = filteredItems
	for _, st := range statuses {
		if !d.sourceActive(st.SourceID) {
			continue
		}
		if st.AuthFailed {
			label, _ := SourceLabel(st.SourceID)
			items = append(items, domain.Item{
				ID: domain.ItemID{SourceID: "watch:auth", ExternalID: st.SourceID}, Kind: domain.KindCredential,
				Title: "AUTH FAILED: " + label, CreatedAt: st.LastError, // error text stays in SourceStatusView.Error (FR-11.3 AC2), not repeated here
				UpdatedAt: st.LastError.Truncate(24 * time.Hour), // dismissal holds for the day, re-appears next day if still failing
				Payload:   domain.MustPayload(domain.CredentialPayload{AuthFailed: true, UsedBy: st.SourceID}),
			})
		}
	}
	day := domain.SnapshotDay(now, d.cfg.Snapshot.Hour, d.cfg.Snapshot.Minute, d.cfg.Server.Location)
	prevBySource := map[string]*domain.Snapshot{}
	out := make([]domain.Evaluated, 0, len(items))
	for _, it := range items {
		prev, ok := prevBySource[it.ID.SourceID]
		if !ok {
			prev, err = d.store.SnapshotBefore(ctx, it.ID.SourceID, day)
			if err != nil {
				return nil, err
			}
			prevBySource[it.ID.SourceID] = prev
		}
		var dis *domain.Dismissal
		if dd, ok := dismissals[it.ID]; ok {
			dis = &dd
		}
		out = append(out, domain.Evaluated{Item: it, Eval: rules.Evaluate(it, prev, dis, now)})
	}
	return out, nil
}

// Build produces the complete view.
func (d *Dashboard) Build(ctx context.Context) (View, error) {
	now := d.clock.Now()
	loc := d.cfg.Server.Location
	evs, err := d.EvaluateAll(ctx)
	if err != nil {
		return View{}, err
	}
	statuses, err := d.store.Statuses(ctx)
	if err != nil {
		return View{}, err
	}
	filteredStatuses := statuses[:0]
	for _, status := range statuses {
		if d.sourceActive(status.SourceID) {
			filteredStatuses = append(filteredStatuses, status)
		}
	}
	statuses = filteredStatuses
	v := View{SchemaVersion: DashboardSchemaVersion, GeneratedAt: now, Tiles: d.cfg.UI.Tiles}
	v.Header = d.header(now, loc, statuses)
	v.Attention = d.attention(evs, now)
	v.Header.AttentionCount = v.Attention.Total
	v.Repos = d.repos(evs, now)
	v.Sites = d.sites(evs)
	v.Watch = d.watch(evs, now)
	return v, nil
}

func (d *Dashboard) header(now time.Time, loc *time.Location, statuses []domain.FetchStatus) HeaderView {
	h := HeaderView{Date: now.In(loc).Format("Mon 2 Jan 2006"), Time: now.In(loc).Format("15:04"), DataAsOf: "never"}
	var latest time.Time
	for _, st := range statuses {
		sv := SourceStatusView{ID: st.SourceID, Kind: st.Kind, Healthy: st.Healthy(), AuthFailed: st.AuthFailed, InFlight: st.InFlight, Error: st.ErrorMsg}
		if !st.LastSuccess.IsZero() {
			sv.Age = HumanAge(st.DataAge(now))
			if st.LastSuccess.After(latest) {
				latest = st.LastSuccess
			}
		} else {
			sv.Age = "never"
		}
		if st.InFlight {
			h.Refreshing++
		}
		h.Sources = append(h.Sources, sv)
	}
	if !latest.IsZero() {
		h.DataAsOf = latest.In(loc).Format("15:04")
	}
	return h
}

func (d *Dashboard) attention(evs []domain.Evaluated, now time.Time) AttentionView {
	list := domain.FilterAttention(evs)
	domain.SortByUrgency(list)
	shown, overflow := domain.Cap(list, d.cfg.UI.AttentionCap)
	av := AttentionView{Total: len(list), Overflow: overflow}
	for _, e := range shown {
		label, short := SourceLabel(e.Item.ID.SourceID)
		row := AttentionRow{ID: e.Item.ID.String(), Source: label, SourceShort: short, Title: e.Item.Title, URL: e.Item.URL,
			Author: e.Item.Author, Age: HumanAge(now.Sub(e.Item.CreatedAt)), Bucket: e.Eval.Bucket.String(),
			Badge: e.Eval.Level.Badge(), Level: e.Eval.Level.String(), Kind: string(e.Item.Kind), UpdatedAt: e.Item.UpdatedAt.Unix()}
		if e.Item.Kind == domain.KindIssue || e.Item.Kind == domain.KindPR {
			if _, num, ok := strings.Cut(e.Item.ID.ExternalID, "/"); ok {
				row.Number = "#" + num
			}
		}
		av.Rows = append(av.Rows, row)
	}
	return av
}

func (d *Dashboard) repos(evs []domain.Evaluated, now time.Time) []RepoCard {
	bySource := map[string][]domain.Evaluated{}
	for _, e := range evs {
		bySource[e.Item.ID.SourceID] = append(bySource[e.Item.ID.SourceID], e)
	}
	cards := make([]RepoCard, 0, len(d.cfg.GitHub.Repos))
	for _, r := range d.cfg.GitHub.Repos {
		_, short := SourceLabel("github:" + r.Name)
		card := RepoCard{Name: r.Name, ShortName: short, URL: "https://github.com/" + r.Name, Build: BuildView{State: "unknown"}}
		var run *domain.Evaluated
		for i, e := range bySource["github:"+r.Name] {
			switch e.Item.Kind {
			case domain.KindIssue:
				card.OpenIssues++
			case domain.KindPR:
				card.OpenPRs++
			case domain.KindWorkflowRun:
				if run == nil || e.Item.CreatedAt.After(run.Item.CreatedAt) {
					run = &bySource["github:"+r.Name][i]
				}
				continue
			default:
				continue
			}
			if e.Eval.New && !e.Eval.Dismissed {
				card.New++
			}
			if e.Eval.Unanswered && !e.Eval.Dismissed {
				card.Unanswered++
			}
		}
		if run != nil {
			// DecodePayload's error is deliberately discarded, same convention and same rationale as
			// domain.Evaluate's WorkflowRun case: a Kind/payload mismatch unmarshals without error
			// (unknown fields are ignored, the rest is zero), so checking err would not catch the
			// realistic corruption mode anyway, and a zero payload just falls through to "unknown".
			p, _ := domain.DecodePayload[domain.WorkflowRunPayload](run.Item)
			card.Build = BuildView{PreviousConclusion: p.PreviousConclusion, Workflow: p.WorkflowName,
				URL: run.Item.URL, Age: HumanAge(now.Sub(run.Item.CreatedAt))}
			switch {
			case p.Status != "completed":
				card.Build.State = "running"
			case p.Conclusion == "success":
				card.Build.State = "ok"
			case p.Conclusion == "failure":
				card.Build.State = "failed"
			default:
				card.Build.State = "unknown"
			}
		}
		cards = append(cards, card)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		ai, aj := cards[i].New+cards[i].Unanswered, cards[j].New+cards[j].Unanswered
		if ai != aj {
			return ai > aj
		}
		return cards[i].Name < cards[j].Name
	})
	return cards
}

func (d *Dashboard) sites(evs []domain.Evaluated) []SiteView {
	views := make([]SiteView, 0, len(d.cfg.Plausible.Sites))
	bySite := make(map[string]SiteView, len(d.cfg.Plausible.Sites))
	for _, evaluated := range evs {
		if evaluated.Item.Kind != domain.KindMetricSeries || !strings.HasPrefix(evaluated.Item.ID.SourceID, "plausible:") {
			continue
		}
		payload, err := domain.DecodePayload[domain.MetricSeriesPayload](evaluated.Item)
		if err != nil {
			continue
		}
		site := strings.TrimPrefix(evaluated.Item.ID.SourceID, "plausible:")
		bySite[site] = SiteView{Site: site, URL: evaluated.Item.URL, Visitors7d: payload.Visitors7d, Visitors30d: payload.Visitors30d,
			Pageviews30d: payload.Pageviews30d, DeltaVisitors7d: payload.DeltaVisitors7d,
			DeltaVisitors30d: payload.DeltaVisitors30d, Daily: payload.Daily, TopPages: payload.TopPages}
	}
	for _, site := range d.cfg.Plausible.Sites {
		if view, ok := bySite[site]; ok {
			views = append(views, view)
		}
	}
	if d.cfg.Plausible.Order == "visitors" {
		sort.SliceStable(views, func(i, j int) bool { return views[i].Visitors7d > views[j].Visitors7d })
	}
	return views
}

func (d *Dashboard) watch(evs []domain.Evaluated, now time.Time) []WatchView {
	views := make([]WatchView, 0)
	for _, evaluated := range evs {
		item := evaluated.Item
		view := WatchView{ID: item.ID.String(), Name: item.Title, Kind: string(item.Kind), State: evaluated.Eval.Level.String(), Badge: evaluated.Eval.Level.Badge(), URL: item.URL}
		switch item.Kind {
		case domain.KindCredential:
			payload, err := domain.DecodePayload[domain.CredentialPayload](item)
			if err != nil {
				continue
			}
			view.UsedBy, view.URL, view.WarnDays = payload.UsedBy, payload.URL, payload.WarnDays
			view.AutoDetected, view.AuthFailed = payload.AutoDetected, payload.AuthFailed
			if payload.Expires != nil {
				view.ExpiresAt = payload.Expires.UTC().Format(time.RFC3339)
				days := int(payload.Expires.Sub(now).Hours() / 24)
				view.RemainingDays = &days
			}
		case domain.KindHealthCheck:
			payload, err := domain.DecodePayload[domain.HealthCheckPayload](item)
			if err != nil {
				continue
			}
			ok := payload.OK
			view.OK = &ok
			view.StatusCode, view.LatencyMs = payload.StatusCode, payload.LatencyMs
			view.ConsecutiveFailures = payload.ConsecutiveFailures
			view.Issuer, view.HostnameValid = payload.CertIssuer, payload.HostnameValid
			if !payload.CheckedAt.IsZero() {
				view.CheckedAt = payload.CheckedAt.UTC().Format(time.RFC3339)
			}
			if !payload.LastOK.IsZero() {
				view.LastOK = payload.LastOK.UTC().Format(time.RFC3339)
			}
			if payload.CertExpires != nil {
				view.ExpiresAt = payload.CertExpires.UTC().Format(time.RFC3339)
				days := int(payload.CertExpires.Sub(now).Hours() / 24)
				view.RemainingDays = &days
			}
		default:
			continue
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].RemainingDays != nil && views[j].RemainingDays != nil && *views[i].RemainingDays != *views[j].RemainingDays {
			return *views[i].RemainingDays < *views[j].RemainingDays
		}
		if views[i].RemainingDays != nil {
			return true
		}
		if views[j].RemainingDays != nil {
			return false
		}
		return views[i].Name < views[j].Name
	})
	return views
}
