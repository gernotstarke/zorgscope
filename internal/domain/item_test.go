package domain

import (
	"testing"
	"time"
)

func TestItemIDRoundTrip(t *testing.T) {
	id := ItemID{SourceID: "github:arc42/arc42-template", ExternalID: "issues/236"}
	s := id.String()
	if s != "github:arc42/arc42-template|issues/236" {
		t.Fatalf("String() = %q", s)
	}
	back, err := ParseItemID(s)
	if err != nil || back != id {
		t.Fatalf("ParseItemID(%q) = %v, %v", s, back, err)
	}
	if _, err := ParseItemID("no-separator"); err == nil {
		t.Fatal("expected error for missing separator")
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	it := Item{Kind: KindPR, Payload: MustPayload(PRPayload{Draft: true, ReviewDecision: "REVIEW_REQUIRED", Comments: 3})}
	p, err := DecodePayload[PRPayload](it)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Draft || p.ReviewDecision != "REVIEW_REQUIRED" || p.Comments != 3 {
		t.Fatalf("payload = %+v", p)
	}
	empty, err := DecodePayload[PRPayload](Item{})
	if err != nil || empty != (PRPayload{}) {
		t.Fatalf("empty payload should decode to zero value, got %+v, %v", empty, err)
	}
	if _, err := DecodePayload[PRPayload](Item{Payload: []byte("{not json")}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestEncodePayloadError(t *testing.T) {
	if _, err := EncodePayload(make(chan int)); err == nil {
		t.Fatal("expected error for unmarshalable value")
	}
	_ = time.Now // keep import used in later edits
}
