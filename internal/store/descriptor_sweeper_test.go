package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestDescriptorSweeper(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "sweeper-test-*.db")
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

	t.Run("expiry_sweeper_removes_old_descriptors", func(t *testing.T) {
		// Add an expired descriptor
		expiredDesc := &model.RelayDescriptor{
			RelayID:      "relay-expired-sweep",
			WSURL:        "wss://expired.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now.Add(-48 * time.Hour),
			LastSeenAt:   now.Add(-48 * time.Hour),
			ExpiresAt:    now.Add(-24 * time.Hour), // Already expired
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, expiredDesc)

		// Add another expired descriptor
		expiredDesc2 := &model.RelayDescriptor{
			RelayID:      "relay-expired-sweep-2",
			WSURL:        "wss://expired2.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now.Add(-25 * time.Hour),
			LastSeenAt:   now.Add(-25 * time.Hour),
			ExpiresAt:    now.Add(-1 * time.Hour), // Expired 1 hour ago
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, expiredDesc2)

		// Add a valid descriptor
		validDesc := &model.RelayDescriptor{
			RelayID:      "relay-valid-sweep",
			WSURL:        "wss://valid.example.com:8080/ws",
			Region:       "us-east-1",
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		store.PutDescriptor(ctx, validDesc)

		// Verify initial count
		count, _ := store.CountDescriptors(ctx)
		if count != 3 {
			t.Errorf("expected 3 descriptors initially, got %d", count)
		}

		// Track expired count for metrics callback
		var expiredCount int
		expiredCtr := func() {
			expiredCount++
		}

		// Create and run sweeper
		sweeper := NewDescriptorSweeper(store, 1*time.Second, 24*time.Hour)
		sweeper.SetExpiredCounter(expiredCtr)

		// Run sweep immediately
		sweeper.sweep(ctx)

		// Check results
		count, _ = store.CountDescriptors(ctx)

		// Should have 1 descriptor remaining (the valid one)
		if count != 1 {
			t.Errorf("expected 1 descriptor after sweep, got %d", count)
		}

		// Check expired counter was called
		if expiredCount != 2 {
			t.Errorf("expected expired counter to be called 2 times, got %d", expiredCount)
		}

		// Verify valid descriptor still exists
		_, err := store.GetDescriptor(ctx, "relay-valid-sweep")
		if err != nil {
			t.Errorf("expected valid descriptor to exist: %v", err)
		}

		// Verify expired descriptors are gone
		_, err = store.GetDescriptor(ctx, "relay-expired-sweep")
		if err == nil {
			t.Error("expected expired descriptor to be removed")
		}
	})

	t.Run("sweeper_handles_empty_store", func(t *testing.T) {
		// Create fresh store
		tmpFile2, err := os.CreateTemp("", "sweeper-empty-test-*.db")
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		tmpPath2 := tmpFile2.Name()
		tmpFile2.Close()
		defer os.Remove(tmpPath2)

		store2 := NewBBoltDescriptorStore(tmpPath2)
		if err := store2.Open(); err != nil {
			t.Fatalf("failed to open store: %v", err)
		}
		defer store2.Close()

		sweeper := NewDescriptorSweeper(store2, 1*time.Second, 24*time.Hour)

		// Run sweep on empty store - should not panic
		sweeper.sweep(ctx)

		count, _ := store2.CountDescriptors(ctx)
		if count != 0 {
			t.Errorf("expected 0 descriptors, got %d", count)
		}
	})
}

func TestDescriptorSweeperStartStop(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "sweeper-start-stop-test-*.db")
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

	sweeper := NewDescriptorSweeper(store, 100*time.Millisecond, 24*time.Hour)

	// Start sweeper
	ctx, cancel := context.WithCancel(context.Background())
	go sweeper.Start(ctx)

	// Let it run briefly
	time.Sleep(200 * time.Millisecond)

	// Stop sweeper
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Sweeper should stop without error
	// If it didn't stop properly, we'd see a panic or error
}
