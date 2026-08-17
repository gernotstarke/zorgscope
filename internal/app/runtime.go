package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// ErrRuntimeNotStarted is returned when a delivery adapter is used before Runtime.Start.
var ErrRuntimeNotStarted = errors.New("zorgscope runtime not started")

type generation struct {
	cfg       *config.Config
	dashboard *Dashboard
	scheduler *Scheduler
	cancel    context.CancelFunc
	done      chan struct{}
}

// Runtime owns the reloadable part of the application. The database and HTTP listener remain
// stable while a validated config update replaces the source scheduler, snapshotter and dashboard
// query as one generation. Handlers take a short read lock only to obtain the current generation;
// upstream work never runs while the lock is held.
type Runtime struct {
	store    ports.Store
	clock    ports.Clock
	registry *Registry
	http     *http.Client
	sink     ports.CredentialSink
	log      *slog.Logger

	applyMu sync.Mutex
	mu      sync.RWMutex
	root    context.Context
	current *generation
}

// NewRuntime creates a reloadable application runtime.
func NewRuntime(store ports.Store, clock ports.Clock, registry *Registry, hc *http.Client, sink ports.CredentialSink, log *slog.Logger) *Runtime {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runtime{store: store, clock: clock, registry: registry, http: hc, sink: sink, log: log}
}

// Start installs the first configuration and starts its background jobs.
func (r *Runtime) Start(ctx context.Context, cfg *config.Config) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.Lock()
	if r.root != nil {
		r.mu.Unlock()
		return errors.New("zorgscope runtime already started")
	}
	r.root = ctx
	r.mu.Unlock()
	return r.applyLocked(cfg)
}

// Apply replaces the active scheduler and dashboard after constructing every new source
// successfully. The old generation is cancelled and drained before the new one starts, preventing
// overlapping writes from old and new source definitions.
func (r *Runtime) Apply(cfg *config.Config) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	return r.applyLocked(cfg)
}

func (r *Runtime) applyLocked(cfg *config.Config) error {
	r.mu.RLock()
	root := r.root
	r.mu.RUnlock()
	if root == nil {
		return ErrRuntimeNotStarted
	}
	var credentials func() []Credential
	if source, ok := r.sink.(interface{ List() []Credential }); ok {
		credentials = source.List
	}
	sources, err := r.registry.Build(Deps{Cfg: cfg, HTTP: r.http, Clock: r.clock, Sink: r.sink, Credentials: credentials, Log: r.log})
	if err != nil {
		return err
	}
	scheduler := NewScheduler(r.store, r.store, r.clock, r.log, time.Duration(cfg.UI.RefreshMinGapSeconds)*time.Second)
	for _, source := range sources {
		scheduler.Add(source.Fetcher, source.Interval)
	}
	snapshotter := NewSnapshotter(r.store, r.store, r.store, scheduler.SourceIDs, cfg.Snapshot.Hour, cfg.Snapshot.Minute,
		cfg.Server.Location, cfg.Snapshot.RetentionDays, r.clock, r.log)
	dashboard := NewDashboardForSources(r.store, r.clock, cfg, scheduler.SourceIDs())

	r.mu.RLock()
	old := r.current
	r.mu.RUnlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	ctx, cancel := context.WithCancel(root)
	gen := &generation{cfg: cfg, dashboard: dashboard, scheduler: scheduler, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	r.current = gen
	r.mu.Unlock()
	go func() {
		defer close(gen.done)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); scheduler.Run(ctx) }()
		go func() { defer wg.Done(); snapshotter.Run(ctx, time.Minute) }()
		wg.Wait()
	}()
	r.log.Info("runtime configured", "component", "runtime", "sources", len(sources))
	return nil
}

func (r *Runtime) generation() (*generation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.current == nil {
		return nil, ErrRuntimeNotStarted
	}
	return r.current, nil
}

// Build returns the cached dashboard view from the active generation.
func (r *Runtime) Build(ctx context.Context) (View, error) {
	g, err := r.generation()
	if err != nil {
		return View{}, err
	}
	return g.dashboard.Build(ctx)
}

// EvaluateAll evaluates the active generation's items for dismiss-all.
func (r *Runtime) EvaluateAll(ctx context.Context) ([]domain.Evaluated, error) {
	g, err := r.generation()
	if err != nil {
		return nil, err
	}
	return g.dashboard.EvaluateAll(ctx)
}

// TriggerAll requests an immediate refresh from the active scheduler.
func (r *Runtime) TriggerAll() int {
	g, err := r.generation()
	if err != nil {
		return 0
	}
	return g.scheduler.TriggerAll()
}

// InFlight returns the active scheduler's in-flight count.
func (r *Runtime) InFlight() int {
	g, err := r.generation()
	if err != nil {
		return 0
	}
	return g.scheduler.InFlight()
}

// Config returns the immutable configuration of the active generation.
func (r *Runtime) Config() (*config.Config, error) {
	g, err := r.generation()
	if err != nil {
		return nil, err
	}
	return g.cfg, nil
}

// Close cancels and drains the active generation.
func (r *Runtime) Close() {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.RLock()
	g := r.current
	r.mu.RUnlock()
	if g != nil {
		g.cancel()
		<-g.done
	}
}
