package main

import (
	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/adapters/plausible"
	"github.com/gernotstarke/zorgscope/internal/adapters/watch"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// registerSources is the single place where source kinds are wired (QS-4.2).
func registerSources(reg *app.Registry) {
	reg.Register(ports.KindGitHubRepo, githubSources)
	reg.Register(ports.KindPlausibleSite, plausibleSources)
	reg.Register(ports.KindWatchCredentials, watchCredentialSources)
	reg.Register(ports.KindWatchURL, watchURLSources)
}

func githubSources(d app.Deps) ([]app.Source, error) {
	cfg := d.Cfg
	if !cfg.GitHub.Enabled {
		return nil, nil
	}
	client := github.NewClient(d.HTTP, cfg.GitHub.BaseURL, cfg.Secrets.GitHubToken, d.Sink)
	var out []app.Source
	names := make([]string, 0, len(cfg.GitHub.Repos))
	for _, r := range cfg.GitHub.Repos {
		f, err := github.NewRepoFetcher(client, r.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, app.Source{Fetcher: f, Interval: cfg.GitHub.RepoInterval(r.Name)})
		names = append(names, r.Name)
	}
	if cfg.GitHub.Mentions {
		out = append(out, app.Source{Fetcher: github.NewMentionsFetcher(client, names), Interval: cfg.GitHub.PollInterval})
	}
	return out, nil
}

func plausibleSources(d app.Deps) ([]app.Source, error) {
	if !d.Cfg.Plausible.Enabled {
		return nil, nil
	}
	client := plausible.NewClient(d.HTTP, d.Cfg.Plausible.BaseURL, d.Cfg.Secrets.PlausibleAPIKey, d.Clock, d.Cfg.Server.Location)
	out := make([]app.Source, 0, len(d.Cfg.Plausible.Sites))
	for _, site := range d.Cfg.Plausible.Sites {
		fetcher, err := plausible.NewSiteFetcher(client, site)
		if err != nil {
			return nil, err
		}
		out = append(out, app.Source{Fetcher: fetcher, Interval: d.Cfg.Plausible.PollInterval})
	}
	return out, nil
}

func watchCredentialSources(d app.Deps) ([]app.Source, error) {
	if !d.Cfg.Watch.Enabled {
		return nil, nil
	}
	manual := make([]watch.Credential, 0, len(d.Cfg.Watch.Credentials))
	for _, configured := range d.Cfg.Watch.Credentials {
		expires := configured.ExpiresAt
		manual = append(manual, watch.Credential{Name: configured.Name, Expires: &expires,
			WarnDays: configured.WarnDays, UsedBy: configured.UsedBy, URL: configured.URL})
	}
	auto := func() []watch.Credential {
		if d.Credentials == nil {
			return nil
		}
		reported := d.Credentials()
		out := make([]watch.Credential, 0, len(reported))
		for _, credential := range reported {
			out = append(out, watch.Credential{Name: credential.Name, Expires: credential.Expires,
				UsedBy: credential.UsedBy, AutoDetected: true})
		}
		return out
	}
	fetcher, err := watch.NewCredentialsFetcher(manual, auto, d.Cfg.Watch.WarnDays, d.Clock)
	if err != nil {
		return nil, err
	}
	return []app.Source{{Fetcher: fetcher, Interval: d.Cfg.Watch.PollInterval}}, nil
}

func watchURLSources(d app.Deps) ([]app.Source, error) {
	if !d.Cfg.Watch.Enabled {
		return nil, nil
	}
	out := make([]app.Source, 0, len(d.Cfg.Watch.URLs))
	for _, configured := range d.Cfg.Watch.URLs {
		fetcher, err := watch.NewURLFetcher(d.HTTP, watch.URLCheck{Name: configured.Name, URL: configured.URL,
			ExpectStatus: configured.ExpectStatus, ExpectBodyContains: configured.ExpectBodyContains}, d.Clock)
		if err != nil {
			return nil, err
		}
		out = append(out, app.Source{Fetcher: fetcher, Interval: configured.PollInterval})
	}
	return out, nil
}
