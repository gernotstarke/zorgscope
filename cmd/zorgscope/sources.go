package main

import (
	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/app"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// registerSources is the single place where source kinds are wired (QS-4.2). M2 adds
// plausible, todoist, feed and watch here.
func registerSources(reg *app.Registry) {
	reg.Register(ports.KindGitHubRepo, githubSources)
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
