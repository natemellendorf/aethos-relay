package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

// mockStore implements store.Store for testing.
type mockStore struct {
	mu           sync.RWMutex
	messages     map[string]*model.Message
	queues       map[string][]*model.Message
	persistCalls int
}

func newMockStore() *mockStore {
	return &mockStore{
		messages: make(map[string]*model.Message),
		queues:   make(map[string][]*model.Message),
	}
}

func (s *mockStore) Open() error  { return nil }
func (s *mockStore) Close() error { return nil }

func (s *mockStore) PersistMessage(_ context.Context, msg *model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[msg.ID] = msg
	s.queues[msg.To] = append(s.queues[msg.To], msg)
	s.persistCalls++
	return nil
}

func (s *mockStore) GetQueuedMessages(_ context.Context, to string, _ int) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*model.Message{}, s.queues[to]...), nil
}

func (s *mockStore) GetMessageByID(_ context.Context, msgID string) (*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.messages[msgID]
	if !ok {
		return nil, fmt.Errorf("message not found: %s", msgID)
	}
	return msg, nil
}

func (s *mockStore) MarkDelivered(_ context.Context, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg, ok := s.messages[msgID]; ok {
		msg.Delivered = true
	}
	return nil
}

func (s *mockStore) RemoveMessage(_ context.Context, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[msgID]
	if !ok {
		return nil
	}
	q := s.queues[msg.To]
	for i, m := range q {
		if m.ID == msgID {
			s.queues[msg.To] = append(q[:i], q[i+1:]...)
			break
		}
	}
	delete(s.messages, msgID)
	return nil
}

func (s *mockStore) GetExpiredMessages(_ context.Context, before time.Time) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var expired []*model.Message
	for _, msg := range s.messages {
		if msg.ExpiresAt.Before(before) {
			expired = append(expired, msg)
		}
	}
	return expired, nil
}

func (s *mockStore) GetLastSweepTime(_ context.Context) (time.Time, error) { return time.Time{}, nil }
func (s *mockStore) SetLastSweepTime(_ context.Context, _ time.Time) error  { return nil }

func (s *mockStore) GetAllRecipientIDs(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.queues))
	for id := range s.queues {
		ids = append(ids, id)
	}
	return ids, nil
}

// newTestPeerManager creates a PeerManager backed by a mockStore for testing.
func newTestPeerManager(st *mockStore) *PeerManager {
	clients := model.NewClientRegistry()
	return NewPeerManager("test-relay", st, clients, time.Hour)
}

// newTestPeer creates a Peer with a buffered Send channel for use in handler tests.
func newTestPeer() *Peer {
	return &Peer{
		ID:          "test-peer",
		URL:         "ws://test",
		Send:        make(chan []byte, 64),
		ConnectedAt: time.Now(),
		Health:      PeerHealth{LastSeen: time.Now()},
		Done:        make(chan struct{}),
	}
}

// TestDiff tests the diff helper function.
func TestDiff(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected []string
	}{
		{
			name:     "empty a",
			a:        []string{},
			b:        []string{"1", "2"},
			expected: []string{},
		},
		{
			name:     "empty b",
			a:        []string{"1", "2"},
			b:        []string{},
			expected: []string{"1", "2"},
		},
		{
			name:     "both empty",
			a:        []string{},
			b:        []string{},
			expected: []string{},
		},
		{
			name:     "all in b",
			a:        []string{"1", "2"},
			b:        []string{"1", "2"},
			expected: []string{},
		},
		{
			name:     "none in b",
			a:        []string{"1", "2"},
			b:        []string{"3", "4"},
			expected: []string{"1", "2"},
		},
		{
			name:     "partial overlap",
			a:        []string{"1", "2", "3"},
			b:        []string{"2", "4"},
			expected: []string{"1", "3"},
		},
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

// TestHandleRelayForward_PersistsValidMessage verifies that a valid, non-expired
// forwarded message is written to the store exactly once.
func TestHandleRelayForward_PersistsValidMessage(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	msg := &model.Message{
		ID:        "msg-valid-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA==",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	pm.handleRelayForward(newTestPeer(), &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.persistCalls != 1 {
		t.Errorf("expected 1 persist call, got %d", st.persistCalls)
	}
	if _, ok := st.messages[msg.ID]; !ok {
		t.Error("message was not found in store after forward")
	}
}

// TestHandleRelayForward_DeduplicatesDuplicateMessage verifies that a message
// already present in the store is not persisted a second time.
func TestHandleRelayForward_DeduplicatesDuplicateMessage(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	msg := &model.Message{
		ID:        "msg-dup-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA==",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	// Pre-populate store so the message already exists.
	_ = st.PersistMessage(context.Background(), msg)
	callsBefore := st.persistCalls

	pm.handleRelayForward(newTestPeer(), &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: msg,
	})

	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.persistCalls != callsBefore {
		t.Errorf("duplicate message was persisted again (calls went from %d to %d)",
			callsBefore, st.persistCalls)
	}
}

// TestHandleRelayForward_RejectsExpiredMessage verifies that a message whose
// ExpiresAt is in the past is silently dropped and never stored.
func TestHandleRelayForward_RejectsExpiredMessage(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	expired := &model.Message{
		ID:        "msg-expired-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA==",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour),
	}

	pm.handleRelayForward(newTestPeer(), &model.RelayForwardFrame{
		Type:    model.FrameTypeRelayForward,
		Message: expired,
	})

	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.persistCalls != 0 {
		t.Errorf("expired message should not be persisted, got %d persist call(s)", st.persistCalls)
	}
}

// TestHandleRelayInventory_SendsRequestForMissingMessages verifies that when a
// peer's advertised inventory contains messages not known locally, the manager
// sends a relay_request frame on the peer's Send channel for those IDs.
func TestHandleRelayInventory_SendsRequestForMissingMessages(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	ctx := context.Background()
	// Store two local messages for "bob".
	for _, id := range []string{"msg-local-1", "msg-local-2"} {
		_ = st.PersistMessage(ctx, &model.Message{
			ID: id, From: "alice", To: "bob",
			ExpiresAt: time.Now().Add(time.Hour),
		})
	}

	peer := newTestPeer()
	// Peer advertises msg-local-1 (shared) and msg-remote-3 (only peer has it).
	pm.handleRelayInventory(peer, &model.RelayInventoryFrame{
		Type:        model.FrameTypeRelayInventory,
		RecipientID: "bob",
		MessageIDs:  []string{"msg-local-1", "msg-remote-3"},
	})

	select {
	case data := <-peer.Send:
		var frame model.RelayRequestFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("could not unmarshal frame from peer.Send: %v", err)
		}
		if frame.Type != model.FrameTypeRelayRequest {
			t.Errorf("expected type %q, got %q", model.FrameTypeRelayRequest, frame.Type)
		}
		if len(frame.MessageIDs) == 0 {
			t.Error("expected at least one message ID in relay_request")
		}
		// msg-local-1 is present in both inventories; msg-local-2 is only local.
		for _, id := range frame.MessageIDs {
			if id == "msg-local-1" {
				t.Errorf("msg-local-1 should not appear in request (already in peer inventory)")
			}
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("timed out waiting for relay_request frame on peer.Send")
	}
}

// TestHandleRelayRequest_SendsForwardFrame verifies that when the manager
// receives a relay_request for a known message, it writes a relay_forward frame
// containing that message to the peer's Send channel.
func TestHandleRelayRequest_SendsForwardFrame(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	msg := &model.Message{
		ID:        "msg-req-1",
		From:      "alice",
		To:        "bob",
		Payload:   "dGVzdA==",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_ = st.PersistMessage(context.Background(), msg)

	peer := newTestPeer()
	pm.handleRelayRequest(peer, &model.RelayRequestFrame{
		Type:       model.FrameTypeRelayRequest,
		MessageIDs: []string{msg.ID},
	})

	select {
	case data := <-peer.Send:
		var frame model.RelayForwardFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("could not unmarshal frame from peer.Send: %v", err)
		}
		if frame.Type != model.FrameTypeRelayForward {
			t.Errorf("expected type %q, got %q", model.FrameTypeRelayForward, frame.Type)
		}
		if frame.Message == nil || frame.Message.ID != msg.ID {
			t.Errorf("expected forwarded message ID %q, got %v", msg.ID, frame.Message)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("timed out waiting for relay_forward frame on peer.Send")
	}
}

// TestHandleInboundPeer_HandshakeAndRegistration verifies the full inbound-peer
// flow: HTTP upgrade → relay_hello → relay_ok → peer registered with the manager.
func TestHandleInboundPeer_HandshakeAndRegistration(t *testing.T) {
	st := newMockStore()
	pm := newTestPeerManager(st)

	// Run the peer manager so it can accept inbound peers.
	go pm.Run()
	t.Cleanup(pm.Stop)

	// Start a test HTTP server backed by the peer manager's inbound handler.
	ts := httptest.NewServer(http.HandlerFunc(pm.HandleInboundPeer))
	t.Cleanup(ts.Close)

	// Dial as a peer.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send relay_hello.
	hello := model.RelayHelloFrame{
		Type:    model.FrameTypeRelayHello,
		RelayID: "peer-relay-id",
		Version: ProtocolVersion,
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatalf("failed to send relay_hello: %v", err)
	}

	// Expect relay_ok.
	var resp map[string]string
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read relay_ok: %v", err)
	}
	if resp["type"] != model.FrameTypeRelayOK {
		t.Errorf("expected type %q, got %q", model.FrameTypeRelayOK, resp["type"])
	}

	// Allow the manager a moment to process the inbound channel.
	time.Sleep(50 * time.Millisecond)

	if peers := pm.GetPeers(); len(peers) == 0 {
		t.Error("expected peer to be registered with PeerManager after handshake")
	}
}
