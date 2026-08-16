// Package clock provides the real clock and a controllable fake (arc42 §8.10).
package clock

import (
	"sync"
	"time"
)

// Real uses time.Now — the only place in the codebase allowed to (besides cmd/).
type Real struct{}

// Now returns the wall clock time in UTC.
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a settable clock for tests. Safe for concurrent use.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake returns a fake clock starting at t.
func NewFake(t time.Time) *Fake { return &Fake{t: t} }

// Now returns the fake clock's current time.
func (f *Fake) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

// Set moves the clock to t.
func (f *Fake) Set(t time.Time) { f.mu.Lock(); f.t = t; f.mu.Unlock() }

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }
