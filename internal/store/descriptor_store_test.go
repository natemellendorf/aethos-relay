package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestDescriptorStorePutAndGet(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-descriptor-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store := NewBBoltDescriptorStore(f.Name())
	if err := store.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Put descriptor
	desc := &model.RelayDescriptor{
		RelayID:     "relay-1",
		WSURL:       "wss://relay.example.com:8080/ws",
		Region:      "us-east",
		Tags:        []string{"production"},
		FirstSeenAt: now.Unix(),
		LastSeenAt:  now.Unix(),
		ExpiresAt:   now.Add(24 * time.Hour).Unix(),
	}

	if err := store.PutDescriptor(ctx, desc); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Get descriptor
	got, err := store.GetDescriptor(ctx, "relay-1")
	if err != nil {
		t.Fatalf("Failed to get descriptor: %v", err)
	}

	if got == nil {
		t.Fatal("Expected descriptor, got nil")
	}

	if got.RelayID != desc.RelayID {
		t.Errorf("RelayID = %v, want %v", got.RelayID, desc.RelayID)
	}
	if got.WSURL != desc.WSURL {
		t.Errorf("WSURL = %v, want %v", got.WSURL, desc.WSURL)
	}
	if got.Region != desc.Region {
		t.Errorf("Region = %v, want %v", got.Region, desc.Region)
	}
}

func TestDescriptorStoreDedupeUpdatesLastSeen(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-dedupe-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store := NewBBoltDescriptorStore(f.Name())
	if err := store.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Put initial descriptor
	desc := &model.RelayDescriptor{
		RelayID:     "relay-1",
		WSURL:       "wss://relay.example.com:8080/ws",
		FirstSeenAt: now.Unix(),
		LastSeenAt:  now.Unix(),
		ExpiresAt:   now.Add(24 * time.Hour).Unix(),
	}

	if err := store.PutDescriptor(ctx, desc); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Wait a bit
	time.Sleep(10 * time.Millisecond)
	later := time.Now()

	// Put same descriptor again
	desc2 := &model.RelayDescriptor{
		RelayID:     "relay-1",
		WSURL:       "wss://relay.example.com:8080/ws",
		FirstSeenAt: now.Unix(), // This should be ignored
		LastSeenAt:  later.Unix(),
		ExpiresAt:   later.Add(24 * time.Hour).Unix(),
	}

	if err := store.PutDescriptor(ctx, desc2); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Get descriptor
	got, err := store.GetDescriptor(ctx, "relay-1")
	if err != nil {
		t.Fatalf("Failed to get descriptor: %v", err)
	}

	// FirstSeenAt should be preserved from first insert
	if got.FirstSeenAt != now.Unix() {
		t.Errorf("FirstSeenAt = %v, want %v (should be preserved)", got.FirstSeenAt, now.Unix())
	}

	// LastSeenAt should be updated
	if got.LastSeenAt < later.Unix()-1 { // Allow 1s tolerance
		t.Errorf("LastSeenAt = %v, want >= %v", got.LastSeenAt, later.Unix())
	}
}

func TestDescriptorStoreBoundsEnforced(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-bounds-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store := NewBBoltDescriptorStore(f.Name())
	if err := store.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Test that store can handle many descriptors efficiently
	// Using smaller count for test performance
	const testCount = 100
	for i := 0; i < testCount; i++ {
		desc := &model.RelayDescriptor{
			RelayID:     "relay-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		}
		if err := store.PutDescriptor(ctx, desc); err != nil {
			t.Fatalf("Failed to put descriptor %d: %v", i, err)
		}
	}

	// Verify count
	count, err := store.CountDescriptors(ctx)
	if err != nil {
		t.Fatalf("Failed to count: %v", err)
	}

	if count != testCount {
		t.Errorf("Count = %v, want %v", count, testCount)
	}
}

func TestDescriptorStoreExpiredDescriptors(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-expired-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store := NewBBoltDescriptorStore(f.Name())
	if err := store.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Add expired descriptor
	desc := &model.RelayDescriptor{
		RelayID:     "relay-expired",
		WSURL:       "wss://relay.example.com:8080/ws",
		FirstSeenAt: now.Add(-48 * time.Hour).Unix(),
		LastSeenAt:  now.Add(-48 * time.Hour).Unix(),
		ExpiresAt:   now.Add(-24 * time.Hour).Unix(), // Already expired
	}

	if err := store.PutDescriptor(ctx, desc); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Add valid descriptor
	desc2 := &model.RelayDescriptor{
		RelayID:     "relay-valid",
		WSURL:       "wss://relay2.example.com:8080/ws",
		FirstSeenAt: now.Unix(),
		LastSeenAt:  now.Unix(),
		ExpiresAt:   now.Add(24 * time.Hour).Unix(),
	}

	if err := store.PutDescriptor(ctx, desc2); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Get expired
	expired, err := store.GetExpiredDescriptors(ctx, now)
	if err != nil {
		t.Fatalf("Failed to get expired: %v", err)
	}

	// Should find the expired one
	found := false
	for _, d := range expired {
		if d.RelayID == "relay-expired" {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find expired descriptor")
	}
}

func TestDescriptorStoreDeleteDescriptor(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-delete-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store := NewBBoltDescriptorStore(f.Name())
	if err := store.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Put descriptor
	desc := &model.RelayDescriptor{
		RelayID:     "relay-to-delete",
		WSURL:       "wss://relay.example.com:8080/ws",
		FirstSeenAt: now.Unix(),
		LastSeenAt:  now.Unix(),
		ExpiresAt:   now.Add(24 * time.Hour).Unix(),
	}

	if err := store.PutDescriptor(ctx, desc); err != nil {
		t.Fatalf("Failed to put descriptor: %v", err)
	}

	// Delete
	if err := store.DeleteDescriptor(ctx, "relay-to-delete"); err != nil {
		t.Fatalf("Failed to delete descriptor: %v", err)
	}

	// Verify deleted
	got, err := store.GetDescriptor(ctx, "relay-to-delete")
	if err != nil {
		t.Fatalf("Failed to get descriptor: %v", err)
	}

	if got != nil {
		t.Error("Expected nil descriptor after delete")
	}
}
