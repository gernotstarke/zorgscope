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
	store ports.Store
	clock ports.Clock
	cfg   *config.Config
}

// NewDashboard creates a dashboard query service.
func NewDashboard(store ports.Store, clock ports.Clock, cfg *config.Config) *Dashboard {
	return &Dashboard{store: store, clock: clock, cfg: cfg}
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
	for _, st := range statuses {
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
	v := View{GeneratedAt: now, Tiles: d.cfg.UI.Tiles}
	v.Header = d.header(now, loc, statuses)
	v.Attention = d.attention(evs, now)
	v.Header.AttentionCount = v.Attention.Total
	v.Repos = d.repos(evs, now)
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
			card.Build = BuildView{Workflow: p.WorkflowName, URL: run.Item.URL, Age: HumanAge(now.Sub(run.Item.CreatedAt))}
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
