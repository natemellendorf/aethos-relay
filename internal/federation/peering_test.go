package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	mu        sync.Mutex
	messages  map[string]*model.Message // msg.ID -> msg
	delivered map[string]bool           // msgID+recipientID -> delivered
	acked     map[string]bool           // msgID+recipientID -> acked
	removed   []string
	persisted int
}

func newMockStore() *mockStore {
	return &mockStore{
		messages:  make(map[string]*model.Message),
		delivered: make(map[string]bool),
		acked:     make(map[string]bool),
	}
}

func (m *mockStore) Open() error  { return nil }
func (m *mockStore) Close() error { return nil }

func (m *mockStore) PersistMessage(_ context.Context, msg *model.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persisted++
	m.messages[msg.ID] = msg
	return nil
}

func (m *mockStore) GetMessageByID(_ context.Context, id string) (*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.messages[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return msg, nil
}

func (m *mockStore) MarkDelivered(_ context.Context, id string, recipientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered[id+"\x00"+recipientID] = true
	if msg, ok := m.messages[id]; ok {
		msg.Delivered = true
	}
	return nil
}

func (m *mockStore) IsDeliveredTo(_ context.Context, id string, recipientID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.delivered[id+"\x00"+recipientID], nil
}

func (m *mockStore) GetQueuedMessages(_ context.Context, to string, limit int) ([]*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Message
	for _, msg := range m.messages {
		if msg.To == to && !m.delivered[msg.ID+"\x00"+to] {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockStore) GetQueuedMessagesRaw(_ context.Context, to string, limit int) ([]*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Message
	for _, msg := range m.messages {
		if msg.To == to {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockStore) MarkAcked(_ context.Context, id string, recipientID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := id + "\x00" + recipientID
	if m.acked[key] {
		return false, nil
	}
	m.acked[key] = true
	return true, nil
}

func (m *mockStore) IsAckedBy(_ context.Context, id string, recipientID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked[id+"\x00"+recipientID], nil
}

func (m *mockStore) RemoveMessage(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, id)
	delete(m.messages, id)
	return nil
}

func (m *mockStore) GetExpiredMessages(_ context.Context, before time.Time) ([]*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Message
	for _, msg := range m.messages {
		if msg.ExpiresAt.Before(before) {
			result = append(result, msg)
		}
	}
	return result, nil
}

func (m *mockStore) GetLastSweepTime(_ context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (m *mockStore) SetLastSweepTime(_ context.Context, _ time.Time) error { return nil }

func (m *mockStore) GetAllRecipientIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]bool)
	var result []string
	for _, msg := range m.messages {
		if !seen[msg.To] {
			seen[msg.To] = true
			result = append(result, msg.To)
		}
	}
	return result, nil
}

func (m *mockStore) GetAllQueuedMessageIDs(_ context.Context, to string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, msg := range m.messages {
		if msg.To == to {
			ids = append(ids, msg.ID)
		}
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestManager creates a PeerManager wired to a mock store.
func newTestManager(t *testing.T) (*PeerManager, *mockStore) {
	t.Helper()
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("test-relay", st, clients, time.Hour)
	go pm.Run()
	return pm, st
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestDiff tests the diff helper function.
func TestDiff(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected []string
	}{
		{"empty a", []string{}, []string{"1", "2"}, []string{}},
		{"empty b", []string{"1", "2"}, []string{}, []string{"1", "2"}},
		{"both empty", []string{}, []string{}, []string{}},
		{"all in b", []string{"1", "2"}, []string{"1", "2"}, []string{}},
		{"none in b", []string{"1", "2"}, []string{"3", "4"}, []string{"1", "2"}},
		{"partial overlap", []string{"1", "2", "3"}, []string{"2", "4"}, []string{"1", "3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := diff(tt.a, tt.b)
			if len(result) != len(tt.expected) {
				t.Errorf("diff() = %v, want %v", result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("diff()[%d] = %v, want %v", i, v, tt.expected[i])
				}
			}
		})
	}
}

// TestHandleRelayForward_StoresAndDeduplicates verifies that a forwarded
// message is persisted exactly once and duplicate forwards are ignored.
func TestHandleRelayForward_StoresAndDeduplicates(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	peer := &Peer{
		ID:   "peer-1",
		Done: make(chan struct{}),
	}

	// First forward – should persist once.
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	if _, err := st.GetMessageByID(context.Background(), "msg-1"); err != nil {
		t.Fatal("expected message to be stored after first forward, got error:", err)
	}
	if st.persisted != 1 {
		t.Fatalf("expected one persist call after first forward, got %d", st.persisted)
	}

	// Second forward of same message – should be deduped (PersistMessage not called again).
	before := st.persisted
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})
	if st.persisted != before {
		t.Errorf("expected duplicate forward to avoid persist call, got %d calls (want %d)", st.persisted, before)
	}
}

func TestDecodeWarningRawPrefix_BoundsAndSanitizes(t *testing.T) {
	leadingWhitespace := strings.Repeat(" ", DecodeWarningRawPrefixLimit+8)
	raw := json.RawMessage([]byte(leadingWhitespace + `{"type":"relay_forward","x":"y"}` + "\n\t" + strings.Repeat("z", DecodeWarningRawPrefixLimit)))

	prefix := decodeWarningRawPrefix(raw)
	if len(prefix) > DecodeWarningRawPrefixLimit {
		t.Fatalf("prefix length exceeded limit: got %d want <= %d", len(prefix), DecodeWarningRawPrefixLimit)
	}
	if strings.ContainsAny(prefix, "\n\r\t") {
		t.Fatalf("prefix should normalize control whitespace, got %q", prefix)
	}
	if !strings.Contains(prefix, `{"type":"relay_forward"`) {
		t.Fatalf("expected trimmed preview to include content, got %q", prefix)
	}
}

func TestDeliverMessage_TracksAndMarksDeliveryIdentity(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-federation-push-identity",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	deliveryID := "bob\x00device-1"
	recipient := &model.Client{
		WayfarerID: "bob",
		DeliveryID: deliveryID,
		Send:       make(chan []byte, 1),
	}
	clients.Register(recipient)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clients.Count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if clients.Count() == 0 {
		t.Fatal("expected recipient to be registered")
	}

	pm.deliverMessage(msg)

	select {
	case <-recipient.Send:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected delivered message frame")
	}

	deliveredToDevice, err := st.IsDeliveredTo(context.Background(), msg.ID, deliveryID)
	if err != nil {
		t.Fatalf("check device delivery state: %v", err)
	}
	if !deliveredToDevice {
		t.Fatal("expected delivery state under device identity")
	}

	deliveredToWayfarer, err := st.IsDeliveredTo(context.Background(), msg.ID, recipient.WayfarerID)
	if err != nil {
		t.Fatalf("check wayfarer delivery state: %v", err)
	}
	if deliveredToWayfarer {
		t.Fatal("did not expect delivery state under fallback wayfarer identity")
	}

	pulled, err := pm.engine.PullForDeliveryIdentity(context.Background(), deliveryID, 10)
	if err != nil {
		t.Fatalf("pull for delivery identity failed: %v", err)
	}
	if len(pulled) != 0 {
		t.Fatalf("expected message to be filtered as delivered for %q, got %d queued", deliveryID, len(pulled))
	}
}

func TestHandleRelayRequest_ForwardIncludesEnvelopeTimestamps(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	createdAt := time.Unix(1_700_000_000, 0).UTC()
	msg := &model.Message{
		ID:        "msg-forward-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	peer := &Peer{ID: "peer-1", Send: make(chan []byte, 1), Done: make(chan struct{})}
	pm.handleRelayRequest(peer, &model.RelayRequestFrame{
		Type:       model.FrameTypeRelayRequest,
		MessageIDs: []string{msg.ID},
	})

	select {
	case data := <-peer.Send:
		var forward model.RelayForwardFrame
		if err := json.Unmarshal(data, &forward); err != nil {
			t.Fatalf("decode relay_forward: %v", err)
		}
		if forward.Envelope == nil {
			t.Fatal("expected relay_forward envelope timestamps")
		}
		if forward.Envelope.CreatedAt != uint64(msg.CreatedAt.UnixMilli()) {
			t.Fatalf("created_at mismatch: got %d want %d", forward.Envelope.CreatedAt, uint64(msg.CreatedAt.UnixMilli()))
		}
		if forward.Envelope.ExpiresAt != uint64(msg.ExpiresAt.UnixMilli()) {
			t.Fatalf("expires_at mismatch: got %d want %d", forward.Envelope.ExpiresAt, uint64(msg.ExpiresAt.UnixMilli()))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected relay_forward frame to be sent")
	}
}

func TestHandleRelayRequest_RemovesInvalidStoredPayload(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	createdAt := time.Unix(1_700_000_000, 0).UTC()
	msg := &model.Message{
		ID:        "msg-request-invalid",
		From:      "alice",
		To:        "bob",
		Payload:   "%%%",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	peer := &Peer{ID: "peer-1", Send: make(chan []byte, 1), Done: make(chan struct{})}
	pm.handleRelayRequest(peer, &model.RelayRequestFrame{
		Type:       model.FrameTypeRelayRequest,
		MessageIDs: []string{msg.ID},
	})

	select {
	case <-peer.Send:
		t.Fatal("expected invalid payload to be skipped")
	default:
	}

	if _, err := st.GetMessageByID(context.Background(), msg.ID); err == nil {
		t.Fatal("expected invalid payload message to be removed")
	}
}

func TestForwardToPeers_ForwardIncludesEnvelopeTimestamps(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	metrics := model.NewPeerMetrics("peer-1")
	metrics.Connected = true
	peer := &Peer{ID: "peer-1", Send: make(chan []byte, 1), Done: make(chan struct{}), Metrics: metrics}

	pm.peersMu.Lock()
	pm.peers[peer.ID] = peer
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	pm.peerMetrics[peer.ID] = metrics
	pm.metricsMu.Unlock()

	createdAt := time.Unix(1_700_000_100, 0).UTC()
	msg := &model.Message{
		ID:        "msg-forward-selected",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(2 * time.Hour),
	}

	pm.ForwardToPeers(msg, "")

	select {
	case data := <-peer.Send:
		var forward model.RelayForwardFrame
		if err := json.Unmarshal(data, &forward); err != nil {
			t.Fatalf("decode relay_forward: %v", err)
		}
		if forward.Envelope == nil {
			t.Fatal("expected relay_forward envelope timestamps")
		}
		if forward.Envelope.CreatedAt != uint64(msg.CreatedAt.UnixMilli()) {
			t.Fatalf("created_at mismatch: got %d want %d", forward.Envelope.CreatedAt, uint64(msg.CreatedAt.UnixMilli()))
		}
		if forward.Envelope.ExpiresAt != uint64(msg.ExpiresAt.UnixMilli()) {
			t.Fatalf("expires_at mismatch: got %d want %d", forward.Envelope.ExpiresAt, uint64(msg.ExpiresAt.UnixMilli()))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected relay_forward frame to be sent")
	}
}

func TestForwardToPeers_RemovesInvalidStoredPayload(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	metrics := model.NewPeerMetrics("peer-1")
	metrics.Connected = true
	peer := &Peer{ID: "peer-1", Send: make(chan []byte, 1), Done: make(chan struct{}), Metrics: metrics}

	pm.peersMu.Lock()
	pm.peers[peer.ID] = peer
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	pm.peerMetrics[peer.ID] = metrics
	pm.metricsMu.Unlock()

	createdAt := time.Unix(1_700_000_100, 0).UTC()
	msg := &model.Message{
		ID:        "msg-forward-invalid",
		From:      "alice",
		To:        "bob",
		Payload:   "%%%",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(2 * time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	pm.ForwardToPeers(msg, "")

	select {
	case <-peer.Send:
		t.Fatal("expected invalid payload to not be forwarded")
	default:
	}

	if _, err := st.GetMessageByID(context.Background(), msg.ID); err == nil {
		t.Fatal("expected invalid payload message to be removed")
	}
}

func TestDeliverMessage_RemovesCorruptPayloadFromQueue(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-deliver-invalid",
		From:      "alice",
		To:        "bob",
		Payload:   "%%%",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	pm.deliverMessage(msg)

	if _, err := st.GetMessageByID(context.Background(), msg.ID); err == nil {
		t.Fatal("expected invalid payload message to be removed")
	}
}

func TestDropCorruptMessageSkipsEmptyMsgID(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	pm.dropCorruptMessage("", "relay_request", "peer-1", errors.New("decode failed"))

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.removed) != 0 {
		t.Fatalf("expected no remove calls for empty msg_id, got %v", st.removed)
	}
}

// TestHandleRelayForward_RejectsExpired verifies that an already-expired
// message is not stored.
func TestHandleRelayForward_RejectsExpired(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "expired-msg",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour), // already expired
	}

	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	if _, err := st.GetMessageByID(context.Background(), "expired-msg"); err == nil {
		t.Error("expected expired message to be rejected, but it was stored")
	}
}

// TestHandleRelayForward_RejectsInvalidFields verifies that messages with
// missing required fields are rejected.
func TestHandleRelayForward_RejectsInvalidFields(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)
	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}

	now := time.Now()
	cases := []struct {
		name string
		msg  *model.Message
	}{
		{"nil message", nil},
		{"empty ID", &model.Message{From: "a", To: "b", Payload: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}},
		{"empty From", &model.Message{ID: "x", To: "b", Payload: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}},
		{"empty To", &model.Message{ID: "x", From: "a", Payload: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}},
		{"empty Payload", &model.Message{ID: "x", From: "a", To: "b", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pm.handleRelayForward(peer, &model.RelayForwardFrame{Message: tc.msg})
			if tc.msg != nil && tc.msg.ID != "" {
				if _, err := st.GetMessageByID(context.Background(), tc.msg.ID); err == nil {
					t.Errorf("expected invalid message (%s) to be rejected, but it was stored", tc.name)
				}
			}
		})
	}
}

func TestHandleRelayForward_RejectsInvalidPayloadB64(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)
	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}

	now := time.Now()
	msg := &model.Message{
		ID:        "invalid-payload-msg",
		From:      "alice",
		To:        "bob",
		Payload:   "%%%",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	if _, err := st.GetMessageByID(context.Background(), msg.ID); err == nil {
		t.Fatal("expected invalid payload to be rejected")
	}
}

func TestHandleRelayForward_CanonicalizesLegacyPayloadBeforePersist(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "normalized-relay-forward",
		From:      "alice",
		To:        "bob",
		Payload:   " \n\tdGVzdA==\r ",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	stored, err := st.GetMessageByID(context.Background(), msg.ID)
	if err != nil {
		t.Fatalf("expected message to be stored: %v", err)
	}
	if stored.Payload != "dGVzdA" {
		t.Fatalf("expected canonical base64url payload, got %q", stored.Payload)
	}
	if _, err := model.DecodePayloadB64(stored.Payload); err != nil {
		t.Fatalf("expected persisted payload to pass strict decode: %v", err)
	}
}

func TestHandleRelayForward_MalformedEnvelopeFallsBackToLegacyMessageTimestamps(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-envelope-expired",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
		Envelope: &model.RelayForwardEnvelopeMetadata{
			CreatedAt: uint64(now.Unix()),
			ExpiresAt: uint64(now.Add(time.Hour).Unix()),
		},
	})

	if _, err := st.GetMessageByID(context.Background(), msg.ID); err != nil {
		t.Fatal("expected valid legacy message timestamps to be accepted when envelope metadata is malformed")
	}
}

func TestDeliverMessage_EncodesPayloadByRecipientPreference(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-federation-encoding-pref",
		From:      "alice",
		To:        "bob",
		Payload:   "-_8",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	urlRecipient := &model.Client{
		WayfarerID: "bob",
		DeliveryID: "bob",
		Send:       make(chan []byte, 1),
	}
	urlRecipient.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64URL)
	clients.Register(urlRecipient)

	stdRecipient := &model.Client{
		WayfarerID: "bob",
		DeliveryID: "bob-device",
		Send:       make(chan []byte, 1),
	}
	stdRecipient.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64)
	clients.Register(stdRecipient)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clients.Count() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if clients.Count() != 2 {
		t.Fatalf("expected two recipients registered, got %d", clients.Count())
	}

	pm.deliverMessage(msg)

	assertPayload := func(ch <-chan []byte, want string) {
		t.Helper()
		select {
		case raw := <-ch:
			var frame model.WSFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode message frame: %v", err)
			}
			if frame.PayloadB64 != want {
				t.Fatalf("payload mismatch: got %q want %q", frame.PayloadB64, want)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected delivered frame")
		}
	}

	assertPayload(urlRecipient.Send, "-_8")
	assertPayload(stdRecipient.Send, "-_8")
}

// TestHandleRelayForward_NoReGossip verifies that receiving a forwarded message
// does not trigger gossip to other peers (loop prevention).
func TestHandleRelayForward_NoReGossip(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	// Register a fake second peer with a buffered send channel so we can detect gossip.
	otherPeer := &Peer{
		ID:   "other-peer",
		Send: make(chan []byte, 10),
		Done: make(chan struct{}),
	}
	pm.peersMu.Lock()
	pm.peers[otherPeer.ID] = otherPeer
	pm.peersMu.Unlock()

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-no-regossip",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	// Give any goroutines a moment to run.
	time.Sleep(20 * time.Millisecond)

	if len(otherPeer.Send) != 0 {
		t.Errorf("expected no re-gossip after forward, but other peer received %d messages", len(otherPeer.Send))
	}
}

// TestHandleRelayAck_UpdatesMetricsWithoutClientDeliveryMutation verifies that
// relay-to-relay acks update peer scoring inputs but do not mark client delivery.
func TestHandleRelayAck_UpdatesMetricsWithoutClientDeliveryMutation(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	msg := &model.Message{
		ID:        "msg-ack-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := st.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist message: %v", err)
	}

	peer := &Peer{
		ID:      "peer-1",
		Metrics: model.NewPeerMetrics("peer-1"),
		Done:    make(chan struct{}),
	}

	beforeAcks := peer.Metrics.AcksTotal
	pm.handleRelayAck(peer, &model.RelayAckFrame{
		Type:        model.FrameTypeRelayAck,
		EnvelopeID:  msg.ID,
		Destination: msg.To,
		Status:      "accepted",
	})

	if peer.Metrics.AcksTotal != beforeAcks+1 {
		t.Fatalf("expected peer ack count to increment, got %d want %d", peer.Metrics.AcksTotal, beforeAcks+1)
	}

	delivered, err := st.IsDeliveredTo(context.Background(), msg.ID, msg.To)
	if err != nil {
		t.Fatalf("check delivery state: %v", err)
	}
	if delivered {
		t.Fatal("relay_ack should not mark client delivery state")
	}
}

// TestHandleRelayInventory_RequestsMissingMessages verifies that when a peer
// announces messages we don't have, we request exactly those IDs.
func TestHandleRelayInventory_RequestsMissingMessages(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	// Pre-populate store with one message for "bob".
	now := time.Now()
	existing := &model.Message{
		ID:        "msg-local",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	_ = st.PersistMessage(context.Background(), existing)

	// Peer announces two messages: one we already have, one we don't.
	peer := &Peer{
		ID:   "peer-1",
		Send: make(chan []byte, 10),
		Done: make(chan struct{}),
	}
	frame := &model.RelayInventoryFrame{
		Type:        model.FrameTypeRelayInventory,
		RecipientID: "bob",
		MessageIDs:  []string{"msg-local", "msg-remote"},
	}
	pm.handleRelayInventory(peer, frame)

	// Should have sent exactly one relay_request for "msg-remote".
	select {
	case data := <-peer.Send:
		var req model.RelayRequestFrame
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatal("failed to unmarshal request:", err)
		}
		if req.Type != model.FrameTypeRelayRequest {
			t.Errorf("expected relay_request, got %q", req.Type)
		}
		if len(req.MessageIDs) != 1 || req.MessageIDs[0] != "msg-remote" {
			t.Errorf("expected request for [msg-remote], got %v", req.MessageIDs)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected relay_request to be sent but none received")
	}
}

func TestReadLoop_MalformedRelayForwardContinuesProcessing(t *testing.T) {
	st := newMockStore()
	clients := model.NewClientRegistry()
	go clients.Run()
	pm := NewPeerManager("relay-a", st, clients, time.Hour)

	now := time.Now()
	_ = st.PersistMessage(context.Background(), &model.Message{
		ID:        "msg-local",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	})

	peerReady := make(chan *Peer, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}

		peer := &Peer{
			ID:   "peer-readloop",
			URL:  r.RemoteAddr,
			Conn: conn,
			Send: make(chan []byte, 1),
			Done: make(chan struct{}),
		}
		peerReady <- peer
		go pm.readLoop(peer)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	var peer *Peer
	select {
	case peer = <-peerReady:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for server peer")
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"relay_forward","message":"bad"}`)); err != nil {
		t.Fatalf("write malformed relay_forward failed: %v", err)
	}

	if err := conn.WriteJSON(model.RelayInventoryFrame{
		Type:        model.FrameTypeRelayInventory,
		RecipientID: "bob",
		MessageIDs:  []string{"msg-local", "msg-remote"},
	}); err != nil {
		t.Fatalf("write relay_inventory failed: %v", err)
	}

	select {
	case data := <-peer.Send:
		var req model.RelayRequestFrame
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatalf("decode relay_request failed: %v", err)
		}
		if req.Type != model.FrameTypeRelayRequest {
			t.Fatalf("expected relay_request, got %q", req.Type)
		}
		if len(req.MessageIDs) != 1 || req.MessageIDs[0] != "msg-remote" {
			t.Fatalf("expected relay_request for msg-remote, got %v", req.MessageIDs)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected relay_request after malformed relay_forward")
	}
}

// TestInboundPeer_HandshakeValidation verifies that inbound peers must present
// a valid relay_hello frame with a non-empty relay_id.
func TestInboundPeer_HandshakeValidation(t *testing.T) {
	pm, _ := newTestManager(t)
	defer pm.Stop()

	upgradeHandler := func(w http.ResponseWriter, r *http.Request) {
		pm.HandleInboundPeer(w, r)
	}
	srv := httptest.NewServer(http.HandlerFunc(upgradeHandler))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	tests := []struct {
		name       string
		hello      interface{}
		expectOpen bool
	}{
		{
			name:       "valid hello accepted",
			hello:      model.RelayHelloFrame{Type: model.FrameTypeRelayHello, RelayID: "peer-b"},
			expectOpen: true,
		},
		{
			name:       "wrong type rejected",
			hello:      model.RelayHelloFrame{Type: "send", RelayID: "peer-b"},
			expectOpen: false,
		},
		{
			name:       "empty relay_id rejected",
			hello:      model.RelayHelloFrame{Type: model.FrameTypeRelayHello, RelayID: ""},
			expectOpen: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/federation", nil)
			if err != nil {
				t.Fatal("dial failed:", err)
			}
			defer conn.Close()

			if err := conn.WriteJSON(tt.hello); err != nil {
				t.Fatal("write hello failed:", err)
			}

			// Attempt to read the response.
			var resp map[string]string
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			readErr := conn.ReadJSON(&resp)

			if tt.expectOpen {
				if readErr != nil {
					t.Errorf("expected hello_ok but got error: %v", readErr)
					return
				}
				if resp["type"] != model.FrameTypeRelayOK {
					t.Errorf("expected type=%q, got %q", model.FrameTypeRelayOK, resp["type"])
				}
				if resp["relay_id"] == "" {
					t.Error("expected non-empty relay_id in hello_ok")
				}
			} else {
				if readErr == nil {
					t.Errorf("expected connection to be closed after invalid hello, but read succeeded with %v", resp)
				}
			}
		})
	}
}

// TestIsRelayFrameType verifies the relay frame type helper in model.
func TestIsRelayFrameType(t *testing.T) {
	relayTypes := []string{
		model.FrameTypeRelayHello,
		model.FrameTypeRelayInventory,
		model.FrameTypeRelayRequest,
		model.FrameTypeRelayForward,
		model.FrameTypeRelayOK,
	}
	for _, ft := range relayTypes {
		if !model.IsRelayFrameType(ft) {
			t.Errorf("IsRelayFrameType(%q) = false, want true", ft)
		}
		if model.IsClientAllowedFrameType(ft) {
			t.Errorf("IsClientAllowedFrameType(%q) = true, want false", ft)
		}
	}

	clientTypes := []string{
		model.FrameTypeHello,
		model.FrameTypeSend,
		model.FrameTypeMessage,
		model.FrameTypeAck,
	}
	for _, ft := range clientTypes {
		if model.IsRelayFrameType(ft) {
			t.Errorf("IsRelayFrameType(%q) = true, want false", ft)
		}
		if !model.IsClientAllowedFrameType(ft) {
			t.Errorf("IsClientAllowedFrameType(%q) = false, want true", ft)
		}
	}

	if model.IsClientAllowedFrameType("") {
		t.Error("IsClientAllowedFrameType(\"\") = true, want false")
	}
}
