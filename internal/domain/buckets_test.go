package domain

import (
	"testing"
	"time"
)

func TestBucketOf(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		age  time.Duration
		want Bucket
	}{
		{0, BucketLT24h},
		{-time.Hour, BucketLT24h}, // clock skew: created "in the future"
		{23 * time.Hour, BucketLT24h},
		{24 * time.Hour, BucketLT7d},
		{6*24*time.Hour + 23*time.Hour, BucketLT7d},
		{7 * 24 * time.Hour, BucketLT30d},
		{29 * 24 * time.Hour, BucketLT30d},
		{30 * 24 * time.Hour, BucketGE30d},
		{400 * 24 * time.Hour, BucketGE30d},
	}
	for _, c := range cases {
		if got := BucketOf(now.Add(-c.age), now); got != c.want {
			t.Errorf("age %v: got %v want %v", c.age, got, c.want)
		}
	}
	if BucketLT24h.String() != "lt24h" || BucketGE30d.Label() != "≥ 30 d" {
		t.Fatalf("String/Label mismatch: %q %q", BucketLT24h.String(), BucketGE30d.Label())
	}
}
