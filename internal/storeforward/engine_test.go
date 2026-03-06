package storeforward

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
	storepkg "github.com/natemellendorf/aethos-relay/internal/store"
)

type engineHarness struct {
	engine        *Engine
	messageStore  *storepkg.BBoltStore
	envelopeStore *storepkg.BBoltEnvelopeStore
	now           time.Time
}

func newEngineHarness(t *testing.T, maxTTL time.Duration) *engineHarness {
	t.Helper()

	dir := t.TempDir()
	messageStore := storepkg.NewBBoltStore(filepath.Join(dir, "relay.db"))
	if err := messageStore.Open(); err != nil {
		t.Fatalf("open message store: %v", err)
	}

	envelopeStore := storepkg.NewBBoltEnvelopeStore(filepath.Join(dir, "relay.db.envelopes"))
	if err := envelopeStore.Open(); err != nil {
		t.Fatalf("open envelope store: %v", err)
	}

	eng := New(messageStore, maxTTL)
	eng.ConfigureFederation("relay-local", envelopeStore)

	baseNow := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	eng.setNowForTests(func() time.Time { return baseNow })

	t.Cleanup(func() {
		_ = envelopeStore.Close()
		_ = messageStore.Close()
	})

	return &engineHarness{
		engine:        eng,
		messageStore:  messageStore,
		envelopeStore: envelopeStore,
		now:           baseNow,
	}
}

func persistTestMessage(t *testing.T, h *engineHarness, msg *model.Message) {
	t.Helper()
	if err := h.engine.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}
}

func TestClientSendPersistsMessageWithTTL(t *testing.T) {
	tests := []struct {
		name       string
		ttlSeconds int
		wantTTL    time.Duration
	}{
		{name: "uses provided ttl", ttlSeconds: 120, wantTTL: 120 * time.Second},
		{name: "defaults when ttl missing", ttlSeconds: 0, wantTTL: time.Hour},
		{name: "clamps overly large ttl", ttlSeconds: 7200, wantTTL: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newEngineHarness(t, time.Hour)
			msg, _ := h.engine.AcceptClientSend("alice", "bob", "ZGF0YQ==", tt.ttlSeconds)
			if err := h.engine.PersistMessage(context.Background(), msg); err != nil {
				t.Fatalf("persist send: %v", err)
			}

			stored, err := h.messageStore.GetMessageByID(context.Background(), msg.ID)
			if err != nil {
				t.Fatalf("get stored message: %v", err)
			}

			if got := stored.ExpiresAt.Sub(stored.CreatedAt); got != tt.wantTTL {
				t.Fatalf("ttl mismatch: got %v want %v", got, tt.wantTTL)
			}
		})
	}
}

func TestPullReturnsQueuedMessages(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	persistTestMessage(t, h, &model.Message{
		ID:        "msg-1",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(10 * time.Minute),
	})
	persistTestMessage(t, h, &model.Message{
		ID:        "msg-2",
		From:      "alice",
		To:        "bob",
		Payload:   "Qg==",
		CreatedAt: h.now.Add(1 * time.Second),
		ExpiresAt: h.now.Add(10 * time.Minute),
	})

	tests := []struct {
		name      string
		limit     int
		wantCount int
	}{
		{name: "respects explicit limit", limit: 1, wantCount: 1},
		{name: "returns full queue", limit: 10, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := h.engine.PullForDeliveryIdentity(context.Background(), "bob", tt.limit)
			if err != nil {
				t.Fatalf("pull queued: %v", err)
			}
			if len(msgs) != tt.wantCount {
				t.Fatalf("pull count mismatch: got %d want %d", len(msgs), tt.wantCount)
			}
		})
	}
}

func TestClientAckTracksSingleDeliveryIdentity(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	msg := &model.Message{
		ID:        "msg-device",
		From:      "alice",
		To:        "wayfarer-1",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(10 * time.Minute),
	}
	persistTestMessage(t, h, msg)

	deviceA := DeliveryIdentity("wayfarer-1", "device-a")
	deviceB := DeliveryIdentity("wayfarer-1", "device-b")

	if err := h.engine.AckClientDelivery(context.Background(), msg.ID, deviceA); err != nil {
		t.Fatalf("ack device-a: %v", err)
	}

	deliveredA, err := h.messageStore.IsDeliveredTo(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("delivered check device-a: %v", err)
	}
	if !deliveredA {
		t.Fatal("device-a should be marked delivered")
	}

	deliveredB, err := h.messageStore.IsDeliveredTo(context.Background(), msg.ID, deviceB)
	if err != nil {
		t.Fatalf("delivered check device-b: %v", err)
	}
	if deliveredB {
		t.Fatal("device-b should not be marked delivered")
	}

	deviceAMessages, err := h.engine.PullForDeliveryIdentity(context.Background(), deviceA, 10)
	if err != nil {
		t.Fatalf("pull device-a: %v", err)
	}
	if len(deviceAMessages) != 0 {
		t.Fatalf("device-a queue should be empty, got %d", len(deviceAMessages))
	}

	deviceBMessages, err := h.engine.PullForDeliveryIdentity(context.Background(), deviceB, 10)
	if err != nil {
		t.Fatalf("pull device-b: %v", err)
	}
	if len(deviceBMessages) != 1 {
		t.Fatalf("device-b queue should still contain message, got %d", len(deviceBMessages))
	}
}

func TestFederationEnvelopePreservesExpiryAndEnforcesHopLimit(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	msg := &model.Message{
		ID:        "env-msg-1",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(30 * time.Minute),
	}

	tests := []struct {
		name    string
		wantHop int
		wantErr error
	}{
		{name: "first forward", wantHop: 1},
		{name: "second forward", wantHop: 2},
		{name: "third forward exceeds max hops", wantErr: model.ErrEnvelopeHopLimitExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := h.engine.PrepareForwardingEnvelope(context.Background(), msg, 2)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare envelope: %v", err)
			}
			if env.CurrentHopCount != tt.wantHop {
				t.Fatalf("hop mismatch: got %d want %d", env.CurrentHopCount, tt.wantHop)
			}
			if !env.ExpiresAt.Equal(msg.ExpiresAt) {
				t.Fatalf("expiry mismatch: got %v want %v", env.ExpiresAt, msg.ExpiresAt)
			}
		})
	}
}

func TestSeenTrackingPreventsLoops(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	msg := &model.Message{
		ID:        "loop-msg-1",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(30 * time.Minute),
	}

	env, err := h.engine.PrepareForwardingEnvelope(context.Background(), msg, 3)
	if err != nil {
		t.Fatalf("prepare envelope: %v", err)
	}

	first, err := h.engine.ReserveForwardingCandidate(context.Background(), env.ID, "relay-peer-1")
	if err != nil {
		t.Fatalf("reserve first candidate: %v", err)
	}
	if !first {
		t.Fatal("first reservation should succeed")
	}

	second, err := h.engine.ReserveForwardingCandidate(context.Background(), env.ID, "relay-peer-1")
	if err != nil {
		t.Fatalf("reserve second candidate: %v", err)
	}
	if second {
		t.Fatal("second reservation to same peer should be rejected")
	}

	forwarded := &model.Message{
		ID:        "loop-msg-2",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(30 * time.Minute),
	}

	firstResult, err := h.engine.AcceptRelayForward(context.Background(), "relay-peer-2", forwarded, 1024)
	if err != nil {
		t.Fatalf("accept first forward: %v", err)
	}
	if firstResult.Status != RelayForwardAccepted {
		t.Fatalf("expected first forward accepted, got %s", firstResult.Status)
	}

	secondResult, err := h.engine.AcceptRelayForward(context.Background(), "relay-peer-2", forwarded, 1024)
	if err != nil {
		t.Fatalf("accept second forward: %v", err)
	}
	if secondResult.Status != RelayForwardSeenLoop {
		t.Fatalf("expected seen loop status, got %s", secondResult.Status)
	}
}

func TestRelayAckTracksForwardingOutcomeOnly(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	msg := &model.Message{
		ID:        "msg-ack-check",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(30 * time.Minute),
	}
	persistTestMessage(t, h, msg)

	h.engine.RecordRelayAck("peer-1", &model.RelayAckFrame{
		Type:        model.FrameTypeRelayAck,
		EnvelopeID:  msg.ID,
		Destination: "bob",
		Status:      "accepted",
	})

	outcome, ok := h.engine.RelayAckFor(msg.ID, "bob", "peer-1")
	if !ok {
		t.Fatal("expected relay ack outcome to be recorded")
	}
	if outcome.Status != "accepted" {
		t.Fatalf("relay ack status mismatch: got %q", outcome.Status)
	}

	clientDelivered, err := h.messageStore.IsDeliveredTo(context.Background(), msg.ID, "bob")
	if err != nil {
		t.Fatalf("check client delivery state: %v", err)
	}
	if clientDelivered {
		t.Fatal("relay_ack must not mutate client ack delivery state")
	}

	queued, err := h.engine.PullForDeliveryIdentity(context.Background(), "bob", 10)
	if err != nil {
		t.Fatalf("pull queued after relay_ack: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("expected message to remain queued for client path, got %d", len(queued))
	}
}

func TestCurrentExpiryBehaviorForPullAndEnvelopeSweep(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	persistTestMessage(t, h, &model.Message{
		ID:        "expired-msg",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ==",
		CreatedAt: h.now.Add(-2 * time.Minute),
		ExpiresAt: h.now.Add(-1 * time.Minute),
	})
	persistTestMessage(t, h, &model.Message{
		ID:        "active-msg",
		From:      "alice",
		To:        "bob",
		Payload:   "Qg==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(10 * time.Minute),
	})

	pulled, err := h.engine.PullForDeliveryIdentity(context.Background(), "bob", 10)
	if err != nil {
		t.Fatalf("pull for expiry behavior: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("expected pull to include queued expired message before sweep, got %+v", pulled)
	}
	foundExpired := false
	foundActive := false
	for _, msg := range pulled {
		if msg.ID == "expired-msg" {
			foundExpired = true
		}
		if msg.ID == "active-msg" {
			foundActive = true
		}
	}
	if !foundExpired || !foundActive {
		t.Fatalf("expected both active and expired messages on pull, got %+v", pulled)
	}

	if err := h.envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:              "env-expired",
		DestinationID:   "bob",
		OpaquePayload:   []byte("expired"),
		OriginRelayID:   "relay-a",
		CurrentHopCount: 1,
		CreatedAt:       h.now.Add(-2 * time.Minute),
		ExpiresAt:       h.now.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("persist expired envelope: %v", err)
	}
	if err := h.envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:              "env-active",
		DestinationID:   "bob",
		OpaquePayload:   []byte("active"),
		OriginRelayID:   "relay-a",
		CurrentHopCount: 1,
		CreatedAt:       h.now,
		ExpiresAt:       h.now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("persist active envelope: %v", err)
	}

	removed, err := h.engine.SweepExpiredEnvelopes(context.Background(), h.now)
	if err != nil {
		t.Fatalf("sweep envelopes: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected exactly one expired envelope removed, got %d", removed)
	}

	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "env-expired"); err == nil {
		t.Fatal("expired envelope should be removed after sweep")
	}
	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "env-active"); err != nil {
		t.Fatalf("active envelope should remain after sweep: %v", err)
	}
}
