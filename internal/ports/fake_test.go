package ports_test

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

func TestFixedClockAdvance(t *testing.T) {
	start := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	c := &ports.FixedClock{T: start}

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	c.Advance(time.Hour)

	want := start.Add(time.Hour)
	if got := c.Now(); !got.Equal(want) {
		t.Errorf("Now() after Advance = %v, want %v", got, want)
	}
}
