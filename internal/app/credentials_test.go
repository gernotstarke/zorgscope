package app

import (
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
)

func TestCredentialBook(t *testing.T) {
	clk := clock.NewFake(t0)
	b := NewCredentialBook(clk)
	exp := t0.Add(24 * time.Hour)
	b.ReportCredential("zorgscope GitHub token", &exp, "zorgscope")
	b.ReportCredential("zorgscope GitHub token", &exp, "zorgscope") // idempotent
	b.ReportCredential("other", nil, "x")
	list := b.List()
	if len(list) != 2 || list[0].Name != "other" || list[1].Expires == nil || !list[1].Expires.Equal(exp) || !list[1].ReportedAt.Equal(t0) {
		t.Fatalf("list = %+v", list)
	}
}
