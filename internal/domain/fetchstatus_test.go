package domain

import (
	"testing"
	"time"
)

func TestFetchStatus(t *testing.T) {
	s := FetchStatus{SourceID: "s"}
	if !s.Healthy() || s.DataAge(now0) != 0 {
		t.Fatal("never fetched: Healthy() (unknown counts as healthy) and zero DataAge")
	}
	s.LastSuccess = now0.Add(-10 * time.Minute)
	if !s.Healthy() || s.DataAge(now0) != 10*time.Minute {
		t.Fatalf("healthy after success: %+v", s)
	}
	s.LastError = now0.Add(-time.Minute)
	s.ErrorMsg = "boom"
	if s.Healthy() {
		t.Fatal("error after last success → unhealthy")
	}
	s.LastSuccess = now0
	if !s.Healthy() {
		t.Fatal("success after error → healthy again")
	}
}
