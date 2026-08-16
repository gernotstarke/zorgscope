package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	t0 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	if !f.Now().Equal(t0) {
		t.Fatal("initial")
	}
	f.Advance(time.Hour)
	if !f.Now().Equal(t0.Add(time.Hour)) {
		t.Fatal("advance")
	}
	f.Set(t0)
	if !f.Now().Equal(t0) {
		t.Fatal("set")
	}
	if (Real{}).Now().IsZero() {
		t.Fatal("real clock")
	}
}
