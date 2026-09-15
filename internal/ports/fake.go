package ports

import "time"

// FixedClock is a test Clock that reports a fixed time until advanced.
type FixedClock struct{ T time.Time }

// Now returns the clock's current time.
func (c *FixedClock) Now() time.Time { return c.T }

// Advance moves the clock forward by d.
func (c *FixedClock) Advance(d time.Duration) { c.T = c.T.Add(d) }
