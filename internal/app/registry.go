package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Source is a fetcher with its poll interval.
type Source struct {
	Fetcher  ports.SourceFetcher
	Interval time.Duration
}

// Deps is what builders get to construct adapters.
type Deps struct {
	Cfg         *config.Config
	HTTP        *http.Client
	Clock       ports.Clock
	Sink        ports.CredentialSink
	Credentials func() []Credential
	Log         *slog.Logger
}

// Builder constructs the sources of one kind from config; it returns nothing when the kind is disabled.
type Builder func(d Deps) ([]Source, error)

// Registry maps source kinds to builders — the single place to register a new kind (QS-4.2).
type Registry struct {
	kinds    []string
	builders map[string]Builder
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{builders: map[string]Builder{}} }

// Register adds a builder for a kind (registration order = build order).
func (r *Registry) Register(kind string, b Builder) {
	if _, dup := r.builders[kind]; !dup {
		r.kinds = append(r.kinds, kind)
	}
	r.builders[kind] = b
}

// Build runs every builder.
func (r *Registry) Build(d Deps) ([]Source, error) {
	var out []Source
	for _, kind := range r.kinds {
		srcs, err := r.builders[kind](d)
		if err != nil {
			return nil, fmt.Errorf("build sources for %s: %w", kind, err)
		}
		out = append(out, srcs...)
	}
	return out, nil
}
