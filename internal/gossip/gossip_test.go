package gossip

import (
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestSelectSample(t *testing.T) {
	now := time.Now()

	// Create test descriptors with varying freshness
	descriptors := []*model.RelayDescriptor{
		{
			RelayID:    "relay-1",
			WSURL:      "wss://relay1.example.com/ws",
			LastSeenAt: now, // Freshest
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-2",
			WSURL:      "wss://relay2.example.com/ws",
			LastSeenAt: now.Add(-30 * time.Minute), // 30 min ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-3",
			WSURL:      "wss://relay3.example.com/ws",
			LastSeenAt: now.Add(-1 * time.Hour), // 1 hour ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-4",
			WSURL:      "wss://relay4.example.com/ws",
			LastSeenAt: now.Add(-2 * time.Hour), // 2 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-5",
			WSURL:      "wss://relay5.example.com/ws",
			LastSeenAt: now.Add(-3 * time.Hour), // 3 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-6",
			WSURL:      "wss://relay6.example.com/ws",
			LastSeenAt: now.Add(-4 * time.Hour), // 4 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-7",
			WSURL:      "wss://relay7.example.com/ws",
			LastSeenAt: now.Add(-5 * time.Hour), // 5 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-8",
			WSURL:      "wss://relay8.example.com/ws",
			LastSeenAt: now.Add(-6 * time.Hour), // 6 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-9",
			WSURL:      "wss://relay9.example.com/ws",
			LastSeenAt: now.Add(-7 * time.Hour), // 7 hours ago
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-10",
			WSURL:      "wss://relay10.example.com/ws",
			LastSeenAt: now.Add(-8 * time.Hour), // Oldest
			ExpiresAt:  now.Add(24 * time.Hour),
		},
	}

	t.Run("gossip_selects_bounded_sample", func(t *testing.T) {
		// Create a gossip engine (we won't use most of its fields for this test)
		engine := &GossipEngine{}

		// Test selecting 5 from 10 descriptors
		sample := engine.selectSample(descriptors, 5)

		// Should return exactly 5 descriptors
		if len(sample) != 5 {
			t.Errorf("expected 5 descriptors, got %d", len(sample))
		}

		// Should include the freshest ones (most should be from the first half)
		freshCount := 0
		for _, d := range sample {
			if d.LastSeenAt.After(now.Add(-2 * time.Hour)) {
				freshCount++
			}
		}

		// Should have at least some fresh descriptors
		if freshCount == 0 {
			t.Error("expected at least some fresh descriptors in sample")
		}

		t.Logf("Sample: %d fresh, %d older", freshCount, len(sample)-freshCount)
	})

	t.Run("sample_size_equals_input_when_less", func(t *testing.T) {
		engine := &GossipEngine{}

		// Input has 3 descriptors, request sample of 10
		input := descriptors[:3]
		sample := engine.selectSample(input, 10)

		// Should return all 3
		if len(sample) != 3 {
			t.Errorf("expected 3 descriptors, got %d", len(sample))
		}
	})

	t.Run("empty_input_returns_nil", func(t *testing.T) {
		engine := &GossipEngine{}
		sample := engine.selectSample(nil, 10)
		if sample != nil {
			t.Error("expected nil for empty input")
		}

		sample = engine.selectSample([]*model.RelayDescriptor{}, 10)
		if sample != nil {
			t.Error("expected nil for empty slice")
		}
	})
}

func TestFilterStale(t *testing.T) {
	now := time.Now()

	descriptors := []*model.RelayDescriptor{
		{
			RelayID:    "relay-fresh",
			WSURL:      "wss://fresh.example.com/ws",
			LastSeenAt: now,
			ExpiresAt:  now.Add(24 * time.Hour),
		},
		{
			RelayID:    "relay-stale",
			WSURL:      "wss://stale.example.com/ws",
			LastSeenAt: now.Add(-2 * time.Hour),
			ExpiresAt:  now.Add(24 * time.Hour),
		},
	}

	t.Run("filters_stale_descriptors", func(t *testing.T) {
		result := FilterStale(descriptors, 1*time.Hour)

		if len(result) != 1 {
			t.Errorf("expected 1 descriptor, got %d", len(result))
		}

		if result[0].RelayID != "relay-fresh" {
			t.Errorf("expected relay-fresh, got %s", result[0].RelayID)
		}
	})
}

func TestEnforceRegistryCap(t *testing.T) {
	now := time.Now()

	descriptors := []*model.RelayDescriptor{
		{RelayID: "relay-1", LastSeenAt: now.Add(-1 * time.Hour)},
		{RelayID: "relay-2", LastSeenAt: now.Add(-2 * time.Hour)},
		{RelayID: "relay-3", LastSeenAt: now.Add(-3 * time.Hour)},
		{RelayID: "relay-4", LastSeenAt: now.Add(-4 * time.Hour)},
		{RelayID: "relay-5", LastSeenAt: now.Add(-5 * time.Hour)},
	}

	t.Run("returns_nil_when_under_cap", func(t *testing.T) {
		result := EnforceRegistryCap(descriptors, 10)
		if result != nil {
			t.Error("expected nil when under cap")
		}
	})

	t.Run("returns_oldest_when_over_cap", func(t *testing.T) {
		// Request cap of 3 from 5 descriptors
		result := EnforceRegistryCap(descriptors, 3)

		// Should return 2 descriptors (to remove)
		if len(result) != 2 {
			t.Errorf("expected 2 descriptors to remove, got %d", len(result))
		}

		// Should be the oldest ones (relay-5 and relay-4)
		// (sorted by last_seen_at ascending)
		if result[0].RelayID != "relay-5" {
			t.Errorf("expected relay-5 first, got %s", result[0].RelayID)
		}
	})
}

func TestGetStaleDescriptors(t *testing.T) {
	now := time.Now()

	descriptors := []*model.RelayDescriptor{
		{
			RelayID:    "relay-1",
			LastSeenAt: now,
		},
		{
			RelayID:    "relay-2",
			LastSeenAt: now.Add(-2 * time.Hour),
		},
	}

	result := GetStaleDescriptors(descriptors, 1*time.Hour)

	if len(result) != 1 {
		t.Errorf("expected 1 stale descriptor, got %d", len(result))
	}

	if result[0].RelayID != "relay-2" {
		t.Errorf("expected relay-2, got %s", result[0].RelayID)
	}
}
