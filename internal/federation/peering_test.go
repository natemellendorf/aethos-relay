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
	mu       sync.Mutex
	messages map[string]*model.Message // msg.ID -> msg
}

func newMockStore() *mockStore {
	return &mockStore{messages: make(map[string]*model.Message)}
}

func (m *mockStore) Open() error  { return nil }
func (m *mockStore) Close() error { return nil }

func (m *mockStore) PersistMessage(_ context.Context, msg *model.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *mockStore) MarkDelivered(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := m.messages[id]; ok {
		msg.Delivered = true
	}
	return nil
}

func (m *mockStore) GetQueuedMessages(_ context.Context, to string, limit int) ([]*model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Message
	for _, msg := range m.messages {
		if msg.To == to && !msg.Delivered {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockStore) RemoveMessage(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
		if !msg.Delivered && !seen[msg.To] {
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
		if msg.To == to && !msg.Delivered {
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
		Payload:   "dGVzdA==",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	peer := &Peer{
		ID:   "peer-1",
		Done: make(chan struct{}),
	}

	// First forward – should persist.
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	if _, err := st.GetMessageByID(context.Background(), "msg-1"); err != nil {
		t.Fatal("expected message to be stored after first forward, got error:", err)
	}

	// Second forward of same message – should be deduped (PersistMessage not called again).
	before := len(st.messages)
	pm.handleRelayForward(peer, &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})
	if len(st.messages) != before {
		t.Error("expected duplicate forward to be ignored, but store size changed")
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
		Payload:   "dGVzdA==",
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
		Payload:   "dGVzdA==",
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
		Payload:   "dGVzdA==",
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
