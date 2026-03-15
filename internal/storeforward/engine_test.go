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

type ingestStoreSpy struct {
	messages     map[string]*model.Message
	persistCount int
	persistErr   error
}

type ingestEnvelopeStoreSpy struct {
	envelopes             map[string]*model.Envelope
	seenByEnvelopeRelay   map[string]map[string]bool
	relayIngestEmitted    map[string]bool
	failMarkSeenRemaining int
	failMarkSeenErr       error
	failAtomicMarkErr     error
}

func newIngestStoreSpy() *ingestStoreSpy {
	return &ingestStoreSpy{messages: map[string]*model.Message{}}
}

func newIngestEnvelopeStoreSpy() *ingestEnvelopeStoreSpy {
	return &ingestEnvelopeStoreSpy{
		envelopes:           map[string]*model.Envelope{},
		seenByEnvelopeRelay: map[string]map[string]bool{},
		relayIngestEmitted:  map[string]bool{},
	}
}

func (s *ingestStoreSpy) Open() error  { return nil }
func (s *ingestStoreSpy) Close() error { return nil }

func (s *ingestStoreSpy) PersistMessage(ctx context.Context, msg *model.Message) error {
	if s.persistErr != nil {
		return s.persistErr
	}
	if msg == nil {
		return errors.New("nil message")
	}

	cp := *msg
	s.messages[msg.ID] = &cp
	s.persistCount++
	return nil
}

func (s *ingestStoreSpy) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	return nil, nil
}

func (s *ingestStoreSpy) GetQueuedMessagesRaw(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	return nil, nil
}

func (s *ingestStoreSpy) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	return nil
}

func (s *ingestStoreSpy) MarkAcked(ctx context.Context, msgID string, recipientID string) (bool, error) {
	return false, nil
}

func (s *ingestStoreSpy) IsDeliveredTo(ctx context.Context, msgID string, recipientID string) (bool, error) {
	return false, nil
}

func (s *ingestStoreSpy) IsAckedBy(ctx context.Context, msgID string, recipientID string) (bool, error) {
	return false, nil
}

func (s *ingestStoreSpy) GetMessageByID(ctx context.Context, msgID string) (*model.Message, error) {
	msg, ok := s.messages[msgID]
	if !ok {
		return nil, errors.New("message not found")
	}
	cp := *msg
	return &cp, nil
}

func (s *ingestStoreSpy) RemoveMessage(ctx context.Context, msgID string) error { return nil }

func (s *ingestStoreSpy) GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error) {
	return nil, nil
}

func (s *ingestStoreSpy) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (s *ingestStoreSpy) SetLastSweepTime(ctx context.Context, t time.Time) error { return nil }

func (s *ingestStoreSpy) GetAllRecipientIDs(ctx context.Context) ([]string, error) { return nil, nil }

func (s *ingestStoreSpy) GetAllQueuedMessageIDs(ctx context.Context, to string) ([]string, error) {
	return nil, nil
}

func (s *ingestEnvelopeStoreSpy) Open() error  { return nil }
func (s *ingestEnvelopeStoreSpy) Close() error { return nil }

func (s *ingestEnvelopeStoreSpy) PersistEnvelope(ctx context.Context, env *model.Envelope) error {
	if env == nil {
		return errors.New("nil envelope")
	}
	cp := *env
	s.envelopes[env.ID] = &cp
	return nil
}

func (s *ingestEnvelopeStoreSpy) GetEnvelopeByID(ctx context.Context, envID string) (*model.Envelope, error) {
	env, ok := s.envelopes[envID]
	if !ok {
		return nil, errors.New("envelope not found")
	}
	cp := *env
	return &cp, nil
}

func (s *ingestEnvelopeStoreSpy) GetEnvelopesByDestination(ctx context.Context, destID string, limit int) ([]*model.Envelope, error) {
	return nil, nil
}

func (s *ingestEnvelopeStoreSpy) GetEnvelopeIDsByDestinationPage(ctx context.Context, destID string, afterCursor string, limit int) ([]string, string, int, error) {
	return nil, "", 0, nil
}

func (s *ingestEnvelopeStoreSpy) RemoveEnvelope(ctx context.Context, envID string) error {
	delete(s.envelopes, envID)
	delete(s.seenByEnvelopeRelay, envID)
	return nil
}

func (s *ingestEnvelopeStoreSpy) GetExpiredEnvelopes(ctx context.Context, before time.Time) ([]*model.Envelope, error) {
	return nil, nil
}

func (s *ingestEnvelopeStoreSpy) MarkSeen(ctx context.Context, envID string, relayID string) error {
	if s.failMarkSeenRemaining > 0 {
		s.failMarkSeenRemaining--
		if s.failMarkSeenErr != nil {
			return s.failMarkSeenErr
		}
		return errors.New("mark seen failed")
	}

	if s.seenByEnvelopeRelay[envID] == nil {
		s.seenByEnvelopeRelay[envID] = map[string]bool{}
	}
	s.seenByEnvelopeRelay[envID][relayID] = true
	return nil
}

func (s *ingestEnvelopeStoreSpy) IsSeenBy(ctx context.Context, envID string, relayID string) (bool, error) {
	if s.seenByEnvelopeRelay[envID] == nil {
		return false, nil
	}
	return s.seenByEnvelopeRelay[envID][relayID], nil
}

func (s *ingestEnvelopeStoreSpy) MarkRelayIngestEmitted(ctx context.Context, itemID string) (bool, error) {
	if itemID == "" {
		return false, errors.New("item_id required")
	}
	if s.relayIngestEmitted[itemID] {
		return false, nil
	}
	s.relayIngestEmitted[itemID] = true
	return true, nil
}

func (s *ingestEnvelopeStoreSpy) MarkSeenAndRelayIngestEmitted(ctx context.Context, itemID string, relayIDs []string) (bool, error) {
	if s.failAtomicMarkErr != nil {
		return false, s.failAtomicMarkErr
	}

	for _, relayID := range relayIDs {
		if err := s.MarkSeen(ctx, itemID, relayID); err != nil {
			return false, err
		}
	}

	return s.MarkRelayIngestEmitted(ctx, itemID)
}

func (s *ingestEnvelopeStoreSpy) IsRelayIngestEmitted(ctx context.Context, itemID string) (bool, error) {
	return s.relayIngestEmitted[itemID], nil
}

func (s *ingestEnvelopeStoreSpy) GetAllDestinationIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (s *ingestEnvelopeStoreSpy) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (s *ingestEnvelopeStoreSpy) SetLastSweepTime(ctx context.Context, t time.Time) error {
	return nil
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
	h.engine.SetAckDrivenSuppression(true)

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

	transitioned, err := h.engine.AckClientDelivery(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("ack device-a: %v", err)
	}
	if !transitioned {
		t.Fatal("expected first ack to transition state")
	}

	deliveredA, err := h.messageStore.IsDeliveredTo(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("delivered check device-a: %v", err)
	}
	if deliveredA {
		t.Fatal("canonical ack should not write legacy delivered state")
	}
	ackedA, err := h.messageStore.IsAckedBy(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("acked check device-a: %v", err)
	}
	if !ackedA {
		t.Fatal("device-a should be marked acked")
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

func TestAckDrivenSuppressionUsesAckStateAndIsIdempotent(t *testing.T) {
	h := newEngineHarness(t, time.Hour)
	h.engine.SetAckDrivenSuppression(true)

	msg := &model.Message{
		ID:        "msg-ack-driven",
		From:      "alice",
		To:        "wayfarer-1",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(10 * time.Minute),
	}
	persistTestMessage(t, h, msg)

	deviceA := DeliveryIdentity("wayfarer-1", "device-a")

	stillVisible, err := h.engine.PullForDeliveryIdentity(context.Background(), deviceA, 10)
	if err != nil {
		t.Fatalf("pull before ack: %v", err)
	}
	if len(stillVisible) != 1 {
		t.Fatalf("canonical mode should not suppress before ack, got %d", len(stillVisible))
	}

	firstTransition, err := h.engine.AckClientDelivery(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if !firstTransition {
		t.Fatal("expected first ack to transition")
	}

	secondTransition, err := h.engine.AckClientDelivery(context.Background(), msg.ID, deviceA)
	if err != nil {
		t.Fatalf("second ack: %v", err)
	}
	if secondTransition {
		t.Fatal("expected second ack to be idempotent")
	}

	deviceAMessages, err := h.engine.PullForDeliveryIdentity(context.Background(), deviceA, 10)
	if err != nil {
		t.Fatalf("pull after ack: %v", err)
	}
	if len(deviceAMessages) != 0 {
		t.Fatalf("device-a queue should be empty after durable ack, got %d", len(deviceAMessages))
	}

	deviceB := DeliveryIdentity("wayfarer-1", "device-b")
	deviceBMessages, err := h.engine.PullForDeliveryIdentity(context.Background(), deviceB, 10)
	if err != nil {
		t.Fatalf("pull device-b: %v", err)
	}
	if len(deviceBMessages) != 1 {
		t.Fatalf("device-b queue should still contain message, got %d", len(deviceBMessages))
	}
}

func TestAckDrivenSuppressionDoesNotUseLegacyStateAfterModeToggle(t *testing.T) {
	h := newEngineHarness(t, time.Hour)

	msg := &model.Message{
		ID:        "msg-toggle",
		From:      "alice",
		To:        "wayfarer-1",
		Payload:   "QQ==",
		CreatedAt: h.now,
		ExpiresAt: h.now.Add(10 * time.Minute),
	}
	persistTestMessage(t, h, msg)

	deviceA := DeliveryIdentity("wayfarer-1", "device-a")
	if err := h.engine.MarkDelivery(context.Background(), msg.ID, deviceA); err != nil {
		t.Fatalf("legacy mark delivery write: %v", err)
	}

	canonicalEngine := New(h.messageStore, time.Hour)
	canonicalEngine.SetAckDrivenSuppression(true)

	messages, err := canonicalEngine.PullForDeliveryIdentity(context.Background(), deviceA, 10)
	if err != nil {
		t.Fatalf("pull after mode toggle: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected canonical mode to ignore legacy delivered state, got %d", len(messages))
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
	if removed != 2 {
		t.Fatalf("expected exactly two expired envelopes removed, got %d", removed)
	}

	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "env-expired"); err == nil {
		t.Fatal("expired envelope should be removed after sweep")
	}
	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "expired-msg"); err == nil {
		t.Fatal("expired message-derived envelope should be removed after sweep")
	}
	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "env-active"); err != nil {
		t.Fatalf("active envelope should remain after sweep: %v", err)
	}
	if _, err := h.envelopeStore.GetEnvelopeByID(context.Background(), "active-msg"); err != nil {
		t.Fatalf("active message-derived envelope should remain after sweep: %v", err)
	}
}

func TestRelayIngestDurableWritePrecedesSignal(t *testing.T) {
	st := newIngestStoreSpy()
	eng := New(st, time.Hour)

	observed := false
	eng.SetRelayIngestObserver(func(ctx context.Context, signal RelayIngestSignal) {
		observed = true
		if signal.ItemID != "item-1" {
			t.Fatalf("unexpected item_id: %q", signal.ItemID)
		}
		if !signal.Trusted {
			t.Fatal("expected trusted relay ingest in authenticated context")
		}
		if _, err := st.GetMessageByID(ctx, signal.ItemID); err != nil {
			t.Fatalf("relay_ingest emitted before durable write: %v", err)
		}
	})

	msg := &model.Message{
		ID:        "item-1",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	result, err := eng.AcceptRelayForward(context.Background(), "relay-peer-a", msg, 1024)
	if err != nil {
		t.Fatalf("accept relay forward: %v", err)
	}
	if result.Status != RelayForwardAccepted {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	if !observed {
		t.Fatal("expected relay_ingest signal after durable write")
	}
	if st.persistCount != 1 {
		t.Fatalf("expected one durable persist, got %d", st.persistCount)
	}
}

func TestRelayIngestDuplicateIsIdempotentByItemID(t *testing.T) {
	st := newIngestStoreSpy()
	eng := New(st, time.Hour)

	signalCount := 0
	eng.SetRelayIngestObserver(func(context.Context, RelayIngestSignal) {
		signalCount++
	})

	msg := &model.Message{
		ID:        "item-dup",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	first, err := eng.AcceptRelayForward(context.Background(), "relay-a", msg, 1024)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if first.Status != RelayForwardAccepted {
		t.Fatalf("expected accepted status, got %s", first.Status)
	}

	second, err := eng.AcceptRelayForward(context.Background(), "relay-b", msg, 1024)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if second.Status != RelayForwardDuplicate {
		t.Fatalf("expected duplicate status, got %s", second.Status)
	}

	if st.persistCount != 1 {
		t.Fatalf("expected one durable write, got %d", st.persistCount)
	}
	if signalCount != 1 {
		t.Fatalf("expected one relay_ingest signal, got %d", signalCount)
	}
}

func TestRelayIngestPersistenceFailureEmitsNoSignal(t *testing.T) {
	st := newIngestStoreSpy()
	st.persistErr = errors.New("disk full")
	eng := New(st, time.Hour)

	signalCount := 0
	eng.SetRelayIngestObserver(func(context.Context, RelayIngestSignal) {
		signalCount++
	})

	msg := &model.Message{
		ID:        "item-fail",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	if _, err := eng.AcceptRelayForward(context.Background(), "relay-a", msg, 1024); err == nil {
		t.Fatal("expected persistence failure")
	}
	if signalCount != 0 {
		t.Fatalf("expected no relay_ingest signal on persistence failure, got %d", signalCount)
	}
}

func TestRelayIngestTrustBoundaryRequiresAuthenticatedRelayContext(t *testing.T) {
	st := newIngestStoreSpy()
	eng := New(st, time.Hour)

	trustedFlags := make([]bool, 0, 2)
	eng.SetRelayIngestObserver(func(_ context.Context, signal RelayIngestSignal) {
		trustedFlags = append(trustedFlags, signal.Trusted)
	})

	unauthMsg := &model.Message{
		ID:        "item-unauth",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	authMsg := &model.Message{
		ID:        "item-auth",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	if _, err := eng.AcceptRelayForward(context.Background(), "", unauthMsg, 1024); err != nil {
		t.Fatalf("unauth ingest unexpectedly failed: %v", err)
	}
	if _, err := eng.AcceptRelayForward(context.Background(), "relay-auth", authMsg, 1024); err != nil {
		t.Fatalf("auth ingest unexpectedly failed: %v", err)
	}

	if len(trustedFlags) != 2 {
		t.Fatalf("expected two relay_ingest signals, got %d", len(trustedFlags))
	}
	if trustedFlags[0] {
		t.Fatal("expected unauthenticated relay context to be untrusted")
	}
	if !trustedFlags[1] {
		t.Fatal("expected authenticated relay context to be trusted")
	}
}

func TestRelayIngestRetriesCompleteDurableBoundaryAndEmitExactlyOnce(t *testing.T) {
	messageStore := newIngestStoreSpy()
	envelopeStore := newIngestEnvelopeStoreSpy()
	envelopeStore.failMarkSeenRemaining = 1
	envelopeStore.failMarkSeenErr = errors.New("injected mark_seen failure")

	eng := New(messageStore, time.Hour)
	eng.ConfigureFederation("relay-local", envelopeStore)

	signalCount := 0
	eng.SetRelayIngestObserver(func(_ context.Context, signal RelayIngestSignal) {
		signalCount++
		if signal.ItemID != "item-retry" {
			t.Fatalf("unexpected item_id: %q", signal.ItemID)
		}
	})

	msg := &model.Message{
		ID:        "item-retry",
		From:      "alice",
		To:        "bob",
		Payload:   "QQ",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	if _, err := eng.AcceptRelayForward(context.Background(), "relay-source", msg, 1024); err == nil {
		t.Fatal("expected first ingest to fail during durable boundary")
	}
	if signalCount != 0 {
		t.Fatalf("expected no relay_ingest signal on partial failure, got %d", signalCount)
	}
	if emitted, _ := envelopeStore.IsRelayIngestEmitted(context.Background(), msg.ID); emitted {
		t.Fatal("relay_ingest marker should not exist after partial failure")
	}

	retry, err := eng.AcceptRelayForward(context.Background(), "relay-source", msg, 1024)
	if err != nil {
		t.Fatalf("retry ingest unexpectedly failed: %v", err)
	}
	if retry.Status != RelayForwardDuplicate {
		t.Fatalf("expected retry status duplicate after source-mark failure path, got %s", retry.Status)
	}
	if signalCount != 1 {
		t.Fatalf("expected exactly one relay_ingest signal after successful retry, got %d", signalCount)
	}
	if emitted, _ := envelopeStore.IsRelayIngestEmitted(context.Background(), msg.ID); !emitted {
		t.Fatal("relay_ingest marker should exist after retry success")
	}

	if _, err := eng.AcceptRelayForward(context.Background(), "relay-source", msg, 1024); err != nil {
		t.Fatalf("idempotent post-retry ingest unexpectedly failed: %v", err)
	}
	if signalCount != 1 {
		t.Fatalf("expected no duplicate relay_ingest signal after marker exists, got %d", signalCount)
	}
}
