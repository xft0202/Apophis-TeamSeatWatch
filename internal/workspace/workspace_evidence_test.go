package workspace

import (
	"testing"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestExchangeSuccessDoesNotProveJoinAvailability(t *testing.T) {
	now := time.Now().UTC()
	got := Recompute(now, []Observation{{ID: "exchange", Endpoint: platform.EndpointExchange, Source: SourcePlatform, Outcome: platform.OutcomeOperational, ObservedAt: now, ExpiresAt: now.Add(time.Hour)}})
	if got.State != StateUnknown {
		t.Fatalf("exchange success produced %q; membership capability remains unknown", got.State)
	}
	join := Observation{ID: "join", Endpoint: platform.EndpointJoin, Source: SourcePlatform, Outcome: platform.OutcomeOperational, ObservedAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}
	if got := Recompute(now.Add(time.Minute), []Observation{join}); got.State != StateOperational {
		t.Fatalf("join capability success produced %q; want operational", got.State)
	}
	terminal := Observation{ID: "terminal", Endpoint: platform.EndpointJoin, Source: SourcePlatform, Outcome: platform.OutcomeDeactivated, ObservedAt: now, ExpiresAt: now.Add(time.Hour)}
	if got := Recompute(now.Add(time.Minute), []Observation{terminal, {ID: "unrelated", Endpoint: platform.EndpointExchange, Source: SourcePlatform, Outcome: platform.OutcomeOperational, ObservedAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}}); got.State != StateDeactivated {
		t.Fatalf("unrelated exchange recovered terminal: %q", got.State)
	}
}
