package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestBBoltEnvelopeStore_PersistenceRoundtrip(t *testing.T) {
	// Create temp file
	tmpFile, err := os.CreateTemp("", "envelope-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Create store
	store := NewBBoltEnvelopeStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create test envelope
	env := &model.Envelope{
		ID:              "test-envelope-1",
		DestinationID:   "recipient-123",
		OpaquePayload:   []byte("test payload data"),
		OriginRelayID:   "relay-1",
		CurrentHopCount: 0,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}

	// Persist envelope
	if err := store.PersistEnvelope(ctx, env); err != nil {
		t.Fatalf("failed to persist envelope: %v", err)
	}

	// Retrieve envelope
	retrieved, err := store.GetEnvelopeByID(ctx, env.ID)
	if err != nil {
		t.Fatalf("failed to get envelope: %v", err)
	}

	if retrieved.ID != env.ID {
		t.Errorf("expected ID %s, got %s", env.ID, retrieved.ID)
	}
	if retrieved.DestinationID != env.DestinationID {
		t.Errorf("expected DestinationID %s, got %s", env.DestinationID, retrieved.DestinationID)
	}
	if string(retrieved.OpaquePayload) != string(env.OpaquePayload) {
		t.Errorf("expected payload %s, got %s", env.OpaquePayload, retrieved.OpaquePayload)
	}
}

func TestBBoltEnvelopeStore_TTLExpiration(t *testing.T) {
	// Create temp file
	tmpFile, err := os.CreateTemp("", "envelope-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Create store
	store := NewBBoltEnvelopeStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create expired envelope
	expiredEnv := &model.Envelope{
		ID:              "expired-envelope",
		DestinationID:   "recipient-123",
		OpaquePayload:   []byte("expired payload"),
		OriginRelayID:   "relay-1",
		CurrentHopCount: 0,
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		ExpiresAt:       time.Now().Add(-24 * time.Hour), // Already expired
	}

	if err := store.PersistEnvelope(ctx, expiredEnv); err != nil {
		t.Fatalf("failed to persist expired envelope: %v", err)
	}

	// Get expired envelopes
	expired, err := store.GetExpiredEnvelopes(ctx, time.Now())
	if err != nil {
		t.Fatalf("failed to get expired envelopes: %v", err)
	}

	if len(expired) != 1 {
		t.Errorf("expected 1 expired envelope, got %d", len(expired))
	}

	// Remove expired envelope
	if err := store.RemoveEnvelope(ctx, expiredEnv.ID); err != nil {
		t.Fatalf("failed to remove envelope: %v", err)
	}

	// Verify removed
	_, err = store.GetEnvelopeByID(ctx, expiredEnv.ID)
	if err == nil {
		t.Error("expected error when getting removed envelope")
	}
}

func TestBBoltEnvelopeStore_Dedupe(t *testing.T) {
	// Create temp file
	tmpFile, err := os.CreateTemp("", "envelope-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Create store
	store := NewBBoltEnvelopeStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	envID := "dedupe-test-envelope"
	relayID := "relay-1"

	// Mark as seen by relay-1
	if err := store.MarkSeen(ctx, envID, relayID); err != nil {
		t.Fatalf("failed to mark seen: %v", err)
	}

	// Check if seen
	seen, err := store.IsSeenBy(ctx, envID, relayID)
	if err != nil {
		t.Fatalf("failed to check seen: %v", err)
	}
	if !seen {
		t.Error("expected envelope to be marked as seen")
	}

	// Check different relay
	seen, err = store.IsSeenBy(ctx, envID, "relay-2")
	if err != nil {
		t.Fatalf("failed to check seen: %v", err)
	}
	if seen {
		t.Error("expected envelope to not be seen by relay-2")
	}
}

func TestBBoltEnvelopeStore_GetEnvelopesByDestination(t *testing.T) {
	// Create temp file
	tmpFile, err := os.CreateTemp("", "envelope-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Create store
	store := NewBBoltEnvelopeStore(tmpPath)
	if err := store.Open(); err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	destID := "destination-123"

	// Persist multiple envelopes for same destination
	for i := 0; i < 5; i++ {
		env := &model.Envelope{
			ID:              "env-" + string(rune('0'+i)),
			DestinationID:   destID,
			OpaquePayload:   []byte("payload " + string(rune('0'+i))),
			OriginRelayID:   "relay-1",
			CurrentHopCount: 0,
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
		}
		if err := store.PersistEnvelope(ctx, env); err != nil {
			t.Fatalf("failed to persist envelope: %v", err)
		}
	}

	// Retrieve envelopes for destination
	envelopes, err := store.GetEnvelopesByDestination(ctx, destID, 10)
	if err != nil {
		t.Fatalf("failed to get envelopes by destination: %v", err)
	}

	if len(envelopes) != 5 {
		t.Errorf("expected 5 envelopes, got %d", len(envelopes))
	}
}
