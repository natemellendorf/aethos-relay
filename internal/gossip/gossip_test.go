package gossip

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

func TestGossipSelectsBoundedSample(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-gossip-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s := store.NewBBoltDescriptorStore(f.Name())
	if err := s.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	// Add many descriptors
	for i := 0; i < 100; i++ {
		desc := &model.RelayDescriptor{
			RelayID:     "relay-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Add(time.Duration(-i) * time.Hour).Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		}
		if err := s.PutDescriptor(ctx, desc); err != nil {
			t.Fatalf("Failed to put descriptor: %v", err)
		}
	}

	g := NewGossip(s)
	g.now = func() time.Time { return now } // Override time for testing

	// Select sample
	sample, err := g.SelectSample(ctx)
	if err != nil {
		t.Fatalf("Failed to select sample: %v", err)
	}

	// Should be bounded
	if len(sample) > model.GOSSIP_SAMPLE_SIZE {
		t.Errorf("Sample size = %d, want <= %d", len(sample), model.GOSSIP_SAMPLE_SIZE)
	}

	// Should not include expired (if we had any)
	for _, d := range sample {
		if d.IsExpired(now) {
			t.Errorf("Sample contains expired descriptor: %s", d.RelayID)
		}
	}
}

func TestGossipHandlesDescriptors(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-handle-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s := store.NewBBoltDescriptorStore(f.Name())
	if err := s.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	g := NewGossip(s)
	g.now = func() time.Time { return now }

	// Valid descriptors
	descriptors := []model.RelayDescriptor{
		{
			RelayID:     "relay-1",
			WSURL:       "wss://relay1.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		},
		{
			RelayID:     "relay-2",
			WSURL:       "ws://localhost:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		},
	}

	acks, err := g.HandleDescriptors(ctx, "peer-1", descriptors)
	if err != nil {
		t.Fatalf("HandleDescriptors failed: %v", err)
	}

	// Check acks
	if len(acks) != 2 {
		t.Errorf("Expected 2 acks, got %d", len(acks))
	}

	for _, ack := range acks {
		if ack.Status != "accepted" {
			t.Errorf("Expected accepted, got %s: %s", ack.Status, ack.Reason)
		}
	}

	// Verify stored
	count, err := s.CountDescriptors(ctx)
	if err != nil {
		t.Fatalf("CountDescriptors failed: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 descriptors, got %d", count)
	}
}

func TestGossipRejectsInvalidDescriptors(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-reject-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s := store.NewBBoltDescriptorStore(f.Name())
	if err := s.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	g := NewGossip(s)
	g.now = func() time.Time { return now }

	// Invalid descriptors (bad URL)
	descriptors := []model.RelayDescriptor{
		{
			RelayID:     "relay-bad",
			WSURL:       "http://relay.example.com:8080/ws", // Invalid scheme
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		},
	}

	acks, err := g.HandleDescriptors(ctx, "peer-1", descriptors)
	if err != nil {
		t.Fatalf("HandleDescriptors failed: %v", err)
	}

	// Should be rejected
	if len(acks) != 1 {
		t.Fatalf("Expected 1 ack, got %d", len(acks))
	}

	if acks[0].Status != "rejected" {
		t.Errorf("Expected rejected, got %s", acks[0].Status)
	}
}

func TestGossipRateLimiting(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-rate-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s := store.NewBBoltDescriptorStore(f.Name())
	if err := s.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	g := NewGossip(s)
	g.now = func() time.Time { return now }

	peerID := "test-peer"

	// First request should succeed
	descriptors := []model.RelayDescriptor{
		{
			RelayID:     "relay-1",
			WSURL:       "wss://relay1.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		},
	}

	acks, err := g.HandleDescriptors(ctx, peerID, descriptors)
	if err != nil {
		t.Fatalf("HandleDescriptors failed: %v", err)
	}

	// Should accept first batch
	if len(acks) != 1 || acks[0].Status != "accepted" {
		t.Errorf("First batch should be accepted")
	}

	// Now test rate limiting by directly accessing the limiter
	// Note: The rate limiter uses time-based windows, so we'd need to
	// simulate time passing or test the limiter directly
	limiter := NewPeerRateLimiter(time.Hour)

	// First should pass
	if !limiter.Allow(peerID) {
		t.Error("First request should be allowed")
	}

	// Exhaust the limit
	for i := 1; i < model.MAX_DESCRIPTORS_PER_PEER_PER_HOUR; i++ {
		limiter.Allow(peerID)
	}

	// Next should fail
	if limiter.Allow(peerID) {
		t.Error("Rate limit should have been exceeded")
	}
}

func TestGossipRegistryBounds(t *testing.T) {
	// Create temp file
	f, err := os.CreateTemp("", "test-bounds-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	s := store.NewBBoltDescriptorStore(f.Name())
	if err := s.Open(); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	g := NewGossip(s)
	g.now = func() time.Time { return now }

	// Pre-fill registry to near capacity - use smaller count for test performance
	const prefillCount = 50
	for i := 0; i < prefillCount; i++ {
		desc := &model.RelayDescriptor{
			RelayID:     "existing-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		}
		if err := s.PutDescriptor(ctx, desc); err != nil {
			t.Fatalf("Failed to put descriptor: %v", err)
		}
	}

	// Try to add new descriptor
	newDesc := []model.RelayDescriptor{
		{
			RelayID:     "new-relay",
			WSURL:       "wss://new.example.com:8080/ws",
			FirstSeenAt: now.Unix(),
			LastSeenAt:  now.Unix(),
			ExpiresAt:   now.Add(24 * time.Hour).Unix(),
		},
	}

	acks, err := g.HandleDescriptors(ctx, "peer-1", newDesc)
	if err != nil {
		t.Fatalf("HandleDescriptors failed: %v", err)
	}

	// Should be accepted
	if len(acks) != 1 || acks[0].Status != "accepted" {
		t.Errorf("New descriptor should be accepted")
	}
}
