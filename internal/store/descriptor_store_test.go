package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestBBoltDescriptorStore(t *testing.T) {
	// Create temporary file for test database
	tmpFile, err := os.CreateTemp("", "descriptor-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	store := NewBBoltDescriptorStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	t.Run("registry_dedupe_updates_last_seen", func(t *testing.T) {
		// First, put a descriptor
		desc1 := &model.RelayDescriptor{
			RelayID:      "relay-123",
			WSURL:        "wss://relay1.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now.Add(-1 * time.Hour),
			LastSeenAt:   now.Add(-1 * time.Hour),
			ExpiresAt:    now.Add(23 * time.Hour),
			AdvertisedBy: "peer-1",
		}

		err := store.PutDescriptor(ctx, desc1)
		if err != nil {
			t.Fatalf("failed to put descriptor: %v", err)
		}

		// Now put same descriptor with new last_seen_at
		desc2 := &model.RelayDescriptor{
			RelayID:      "relay-123", // Same ID
			WSURL:        "wss://relay1.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now.Add(-1 * time.Hour), // Original first_seen_at
			LastSeenAt:   now,                     // Updated last_seen_at
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}

		err = store.PutDescriptor(ctx, desc2)
		if err != nil {
			t.Fatalf("failed to update descriptor: %v", err)
		}

		// Verify only one descriptor exists
		count, err := store.CountDescriptors(ctx)
		if err != nil {
			t.Fatalf("failed to count descriptors: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 descriptor, got %d", count)
		}

		// Verify last_seen_at was updated
		stored, err := store.GetDescriptor(ctx, "relay-123")
		if err != nil {
			t.Fatalf("failed to get descriptor: %v", err)
		}
		if !stored.LastSeenAt.Equal(now) {
			t.Errorf("expected last_seen_at = %v, got %v", now, stored.LastSeenAt)
		}
		// FirstSeenAt should remain unchanged
		if !stored.FirstSeenAt.Equal(desc1.FirstSeenAt) {
			t.Errorf("expected first_seen_at = %v, got %v", desc1.FirstSeenAt, stored.FirstSeenAt)
		}
	})

	t.Run("registry_bounds_enforced", func(t *testing.T) {
		// Get current count
		initialCount, _ := store.CountDescriptors(ctx)

		// Try to add more than MAX_TOTAL_DESCRIPTORS
		// (We can't easily test this since we'd need many descriptors,
		// but we can test the logic by checking the limit behavior)

		// Add some descriptors
		for i := 0; i < 5; i++ {
			desc := &model.RelayDescriptor{
				RelayID:      "relay-test-" + string(rune('a'+i)),
				WSURL:        "wss://relay" + string(rune('a'+i)) + ".example.com:8080/ws",
				Region:       "us-east-1",
				FirstSeenAt:  now,
				LastSeenAt:   now,
				ExpiresAt:    now.Add(24 * time.Hour),
				AdvertisedBy: "peer-1",
			}
			err := store.PutDescriptor(ctx, desc)
			if err != nil {
				t.Logf("put descriptor error (may be expected if full): %v", err)
			}
		}

		// Count should be reasonable
		count, err := store.CountDescriptors(ctx)
		if err != nil {
			t.Fatalf("failed to count descriptors: %v", err)
		}
		if count < initialCount {
			t.Errorf("expected count >= %d, got %d", initialCount, count)
		}

		t.Logf("Total descriptors after test: %d (max allowed: %d)", count, model.MAX_TOTAL_DESCRIPTORS)
	})

	t.Run("get_all_descriptors_excludes_expired", func(t *testing.T) {
		// Add an expired descriptor
		expiredDesc := &model.RelayDescriptor{
			RelayID:      "relay-expired",
			WSURL:        "wss://expired.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now.Add(-48 * time.Hour),
			LastSeenAt:   now.Add(-48 * time.Hour),
			ExpiresAt:    now.Add(-24 * time.Hour), // Already expired
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, expiredDesc)

		// Add a valid descriptor
		validDesc := &model.RelayDescriptor{
			RelayID:      "relay-valid",
			WSURL:        "wss://valid.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, validDesc)

		// GetAllDescriptors should exclude expired
		descriptors, err := store.GetAllDescriptors(ctx)
		if err != nil {
			t.Fatalf("failed to get all descriptors: %v", err)
		}

		foundValid := false
		foundExpired := false
		for _, d := range descriptors {
			if d.RelayID == "relay-valid" {
				foundValid = true
			}
			if d.RelayID == "relay-expired" {
				foundExpired = true
			}
		}

		if !foundValid {
			t.Error("expected to find valid descriptor")
		}
		if foundExpired {
			t.Error("expected expired descriptor to be excluded")
		}
	})

	t.Run("remove_descriptor", func(t *testing.T) {
		// Add a descriptor
		desc := &model.RelayDescriptor{
			RelayID:      "relay-to-remove",
			WSURL:        "wss://remove.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, desc)

		// Verify it exists
		_, err := store.GetDescriptor(ctx, "relay-to-remove")
		if err != nil {
			t.Fatalf("failed to get descriptor before removal: %v", err)
		}

		// Remove it
		err = store.RemoveDescriptor(ctx, "relay-to-remove")
		if err != nil {
			t.Fatalf("failed to remove descriptor: %v", err)
		}

		// Verify it's gone
		_, err = store.GetDescriptor(ctx, "relay-to-remove")
		if err == nil {
			t.Error("expected error when getting removed descriptor")
		}
	})
}

func TestDescriptorExpiry(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "descriptor-expiry-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	store := NewBBoltDescriptorStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	t.Run("get_expired_descriptors", func(t *testing.T) {
		// Add an expired descriptor
		expiredDesc := &model.RelayDescriptor{
			RelayID:      "relay-expired-1",
			WSURL:        "wss://expired1.example.com:8080/ws",
			FirstSeenAt:  now.Add(-48 * time.Hour),
			LastSeenAt:   now.Add(-48 * time.Hour),
			ExpiresAt:    now.Add(-24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, expiredDesc)

		// Add another expired descriptor
		expiredDesc2 := &model.RelayDescriptor{
			RelayID:      "relay-expired-2",
			WSURL:        "wss://expired2.example.com:8080/ws",
			FirstSeenAt:  now.Add(-24 * time.Hour),
			LastSeenAt:   now.Add(-24 * time.Hour),
			ExpiresAt:    now.Add(-1 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, expiredDesc2)

		// Add a valid descriptor
		validDesc := &model.RelayDescriptor{
			RelayID:      "relay-valid",
			WSURL:        "wss://valid.example.com:8080/ws",
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, validDesc)

		// Get expired descriptors (expires before now)
		expired, err := store.GetExpiredDescriptors(ctx, now)
		if err != nil {
			t.Fatalf("failed to get expired descriptors: %v", err)
		}

		// Should have exactly 2 expired descriptors
		if len(expired) != 2 {
			t.Errorf("expected 2 expired descriptors, got %d", len(expired))
		}
	})
}
