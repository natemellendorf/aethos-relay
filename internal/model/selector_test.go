package model

import (
	"testing"
)

func TestRelaySelectorIncreasesOnUnhealthy(t *testing.T) {
	selector := NewRelaySelector()

	// Record many failures (unhealthy signals)
	for i := 0; i < 10; i++ {
		selector.RecordPublishAttempt(false)
	}

	// Width should increase
	if selector.CurrentWidth() <= MIN_PUBLISH_WIDTH {
		t.Errorf("expected width to increase on failures, got %d", selector.CurrentWidth())
	}
}

func TestRelaySelectorDecreasesOnStable(t *testing.T) {
	selector := NewRelaySelector()

	// Record many successes (healthy signals)
	for i := 0; i < 10; i++ {
		selector.RecordPublishAttempt(true)
	}

	// Width should decrease
	if selector.CurrentWidth() >= MAX_PUBLISH_WIDTH {
		t.Errorf("expected width to decrease on successes, got %d", selector.CurrentWidth())
	}
}

func TestRelaySelectorClampsToBounds(t *testing.T) {
	selector := NewRelaySelector()

	// Set to minimum
	if selector.CurrentWidth() != MIN_PUBLISH_WIDTH {
		t.Errorf("expected initial width = %d, got %d", MIN_PUBLISH_WIDTH, selector.CurrentWidth())
	}

	// Try to force above max with failures
	for i := 0; i < 50; i++ {
		selector.RecordPublishAttempt(false)
	}

	// Should clamp to max
	if selector.CurrentWidth() > MAX_PUBLISH_WIDTH {
		t.Errorf("expected width clamped to %d, got %d", MAX_PUBLISH_WIDTH, selector.CurrentWidth())
	}

	// Now force below min with successes
	selector2 := NewRelaySelector()
	for i := 0; i < 50; i++ {
		selector2.RecordPublishAttempt(true)
	}

	// Should clamp to min
	if selector2.CurrentWidth() < MIN_PUBLISH_WIDTH {
		t.Errorf("expected width clamped to %d, got %d", MIN_PUBLISH_WIDTH, selector2.CurrentWidth())
	}
}

func TestRelaySelectorWidthBoundaries(t *testing.T) {
	selector := NewRelaySelector()

	// Test boundaries
	if selector.CurrentWidth() < MIN_PUBLISH_WIDTH || selector.CurrentWidth() > MAX_PUBLISH_WIDTH {
		t.Errorf("width %d outside bounds [%d, %d]", selector.CurrentWidth(), MIN_PUBLISH_WIDTH, MAX_PUBLISH_WIDTH)
	}
}

func TestSelectRelaysByScore(t *testing.T) {
	relays := []*RelayScore{
		{RelayID: "relay1", Score: 0.3},
		{RelayID: "relay2", Score: 0.9},
		{RelayID: "relay3", Score: 0.5},
		{RelayID: "relay4", Score: 0.7},
	}

	// Select top 2
	selected := SelectRelays(relays, 2)

	if len(selected) != 2 {
		t.Errorf("expected 2 relays, got %d", len(selected))
	}

	// Should be highest scoring
	if selected[0].RelayID != "relay2" {
		t.Errorf("expected relay2 as first, got %s", selected[0].RelayID)
	}
	if selected[1].RelayID != "relay4" {
		t.Errorf("expected relay4 as second, got %s", selected[1].RelayID)
	}
}

func TestSelectRelaysWithFewerAvailable(t *testing.T) {
	relays := []*RelayScore{
		{RelayID: "relay1", Score: 0.9},
	}

	// Request more than available
	selected := SelectRelays(relays, 5)

	if len(selected) != 1 {
		t.Errorf("expected 1 relay, got %d", len(selected))
	}
}

func TestSelectRelaysWithEmptyList(t *testing.T) {
	var relays []*RelayScore

	selected := SelectRelays(relays, 5)

	if len(selected) != 0 {
		t.Errorf("expected 0 relays, got %d", len(selected))
	}
}

func TestSortByScore(t *testing.T) {
	relays := []*RelayScore{
		{RelayID: "low", Score: 0.2},
		{RelayID: "high", Score: 0.9},
		{RelayID: "mid", Score: 0.5},
	}

	SortByScore(relays)

	// Should be sorted descending
	if relays[0].RelayID != "high" {
		t.Errorf("expected high first, got %s", relays[0].RelayID)
	}
	if relays[1].RelayID != "mid" {
		t.Errorf("expected mid second, got %s", relays[1].RelayID)
	}
	if relays[2].RelayID != "low" {
		t.Errorf("expected low third, got %s", relays[2].RelayID)
	}
}

func TestAdaptiveWidthMixedSignals(t *testing.T) {
	selector := NewRelaySelector()

	// Mixed: 4 failures, 1 success = 80% failure rate
	selector.RecordPublishAttempt(false)
	selector.RecordPublishAttempt(false)
	selector.RecordPublishAttempt(false)
	selector.RecordPublishAttempt(false)
	selector.RecordPublishAttempt(true)

	// With >50% failure, width should increase
	if selector.CurrentWidth() <= MIN_PUBLISH_WIDTH {
		t.Errorf("expected width increase on high failure rate, got %d", selector.CurrentWidth())
	}
}

func TestAdaptiveWidthStable(t *testing.T) {
	selector := NewRelaySelector()

	// 1 failure in 10 attempts = 10% failure rate
	for i := 0; i < 9; i++ {
		selector.RecordPublishAttempt(true)
	}
	selector.RecordPublishAttempt(false)

	// With low failure rate, width should decrease or stay stable
	original := selector.CurrentWidth()
	for i := 0; i < 10; i++ {
		selector.RecordPublishAttempt(true)
	}

	// Should have decreased
	if selector.CurrentWidth() > original {
		t.Errorf("expected width to stay or decrease on stability, got %d (was %d)", selector.CurrentWidth(), original)
	}
}
