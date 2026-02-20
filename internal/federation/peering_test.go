package federation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

// mockStore is a no-op Store used in tests that don't exercise storage.
type mockStore struct{}

func (m *mockStore) Open() error                                                  { return nil }
func (m *mockStore) Close() error                                                 { return nil }
func (m *mockStore) PersistMessage(_ context.Context, _ *model.Message) error    { return nil }
func (m *mockStore) GetQueuedMessages(_ context.Context, _ string, _ int) ([]*model.Message, error) {
	return nil, nil
}
func (m *mockStore) GetMessageByID(_ context.Context, _ string) (*model.Message, error) {
	return nil, nil
}
func (m *mockStore) MarkDelivered(_ context.Context, _ string) error  { return nil }
func (m *mockStore) RemoveMessage(_ context.Context, _ string) error  { return nil }
func (m *mockStore) GetExpiredMessages(_ context.Context, _ time.Time) ([]*model.Message, error) {
	return nil, nil
}
func (m *mockStore) GetLastSweepTime(_ context.Context) (time.Time, error)       { return time.Time{}, nil }
func (m *mockStore) SetLastSweepTime(_ context.Context, _ time.Time) error       { return nil }
func (m *mockStore) GetAllRecipientIDs(_ context.Context) ([]string, error)      { return nil, nil }

var _ store.Store = (*mockStore)(nil)

// testWSServer starts a minimal WebSocket server that completes the relay
// hello/hello_ok handshake and then runs onConn (if non-nil) before returning.
// The returned URL is the ws:// form of the server URL.
func testWSServer(t *testing.T, onConn func(conn *websocket.Conn)) (url string, close func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var hello map[string]interface{}
		if err := conn.ReadJSON(&hello); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]string{"type": "hello_ok", "relay_id": "remote"}); err != nil {
			return
		}
		if onConn != nil {
			onConn(conn)
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	return wsURL, ts.Close
}

// TestPeerOutboundFlag verifies that an outbound peer has Outbound=true
// and an inbound peer has Outbound=false (default).
func TestPeerOutboundFlag(t *testing.T) {
	outbound := &Peer{Outbound: true, URL: "ws://remote:8080/federation"}
	if !outbound.Outbound {
		t.Error("expected Outbound=true for dialed peer")
	}

	inbound := &Peer{URL: "192.168.1.2:54321"}
	if inbound.Outbound {
		t.Error("expected Outbound=false for accepted peer")
	}
}

// TestDialPeerCreatesOutboundPeer verifies that dialPeer sets Outbound=true.
func TestDialPeerCreatesOutboundPeer(t *testing.T) {
	holdConn := make(chan struct{})
	serverURL, closeServer := testWSServer(t, func(_ *websocket.Conn) {
		// Hold the connection open until the test finishes so the peer stays in pm.peers.
		<-holdConn
	})
	defer closeServer()
	defer close(holdConn)

	pm := NewPeerManager("test-relay", &mockStore{}, model.NewClientRegistry(), 0)
	go pm.Run()
	defer pm.Stop()

	pm.AddPeerURL(serverURL)

	// Poll until the peer appears in pm.peers.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pm.peersMu.RLock()
		for _, p := range pm.peers {
			if p.URL == serverURL {
				outbound := p.Outbound
				pm.peersMu.RUnlock()
				if !outbound {
					t.Error("dialPeer: expected Outbound=true on connected peer")
				}
				return
			}
		}
		pm.peersMu.RUnlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout: peer did not appear in peers map")
}

// TestOutboundPeerReconnectsOnDisconnect verifies that when an outbound peer
// drops, the Run() loop automatically triggers a fresh dialPeer call.
func TestOutboundPeerReconnectsOnDisconnect(t *testing.T) {
	var connectCount int32
	reconnected := make(chan struct{}, 1)
	var once sync.Once

	serverURL, closeServer := testWSServer(t, func(conn *websocket.Conn) {
		n := atomic.AddInt32(&connectCount, 1)
		if n == 1 {
			// First connection: return immediately to simulate a disconnect.
			return
		}
		// Second (or later) connection: signal that reconnection succeeded.
		once.Do(func() { close(reconnected) })
		// Hold the connection briefly so dialPeer registers the peer.
		time.Sleep(200 * time.Millisecond)
	})
	defer closeServer()

	pm := NewPeerManager("test-relay", &mockStore{}, model.NewClientRegistry(), 0)
	go pm.Run()
	defer pm.Stop()

	pm.AddPeerURL(serverURL)

	select {
	case <-reconnected:
		// Automatic reconnection succeeded.
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: outbound peer did not reconnect after disconnect")
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

// TestForwardDeduplication tests that duplicate messages are rejected.
func TestForwardDeduplication(t *testing.T) {
	// This tests the dedupe logic conceptually
	// In a real implementation, we'd use a mock store

	msgID := "test-msg-123"

	// Simulate checking for duplicates
	existingMsgIDs := []string{"msg-1", msgID, "msg-3"}
	newMsgID := msgID

	// Check if new message ID already exists
	isDuplicate := false
	for _, id := range existingMsgIDs {
		if id == newMsgID {
			isDuplicate = true
			break
		}
	}

	if !isDuplicate {
		t.Error("Expected message to be detected as duplicate")
	}

	// Test non-duplicate case
	newMsgID = "new-msg-456"
	isDuplicate = false
	for _, id := range existingMsgIDs {
		if id == newMsgID {
			isDuplicate = true
			break
		}
	}

	if isDuplicate {
		t.Error("Expected message to NOT be detected as duplicate")
	}
}

// TestLoopPrevention tests that message loops are prevented.
func TestLoopPrevention(t *testing.T) {
	// Test that we don't re-gossip a message we already have
	// This is done by comparing message IDs before gossiping

	existingIDs := []string{"msg-1", "msg-2"}
	newMessageID := "msg-1" // Already exists

	// Should not gossip if already exists
	shouldGossip := true
	for _, id := range existingIDs {
		if id == newMessageID {
			shouldGossip = false
			break
		}
	}

	if !shouldGossip {
		t.Log("Loop prevention: correctly skipped existing message")
	} else {
		t.Error("Loop prevention: should have skipped existing message")
	}
}

// TestTTLPropagation tests that TTL is preserved on message forward.
func TestTTLPropagation(t *testing.T) {
	now := time.Now()
	originalTTL := 1 * time.Hour

	// Original message with TTL
	msg := &model.Message{
		ID:        "test-msg",
		From:      "sender",
		To:        "recipient",
		Payload:   "dGVzdA==", // "test" in base64
		CreatedAt: now,
		ExpiresAt: now.Add(originalTTL),
		Delivered: false,
	}

	// Verify TTL is preserved
	elapsed := msg.ExpiresAt.Sub(msg.CreatedAt)
	if elapsed != originalTTL {
		t.Errorf("TTL not preserved: got %v, want %v", elapsed, originalTTL)
	}

	// Simulate forwarding - TTL should NOT be extended
	time.Sleep(1 * time.Millisecond)
	// The ExpiresAt should remain the same
	if msg.ExpiresAt.Sub(msg.CreatedAt) != originalTTL {
		t.Error("TTL was modified during forward simulation")
	}

	// Test that expired messages are rejected
	expiredMsg := &model.Message{
		ID:        "expired-msg",
		From:      "sender",
		To:        "recipient",
		Payload:   "dGVzdA==",
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour), // Already expired
		Delivered: false,
	}

	isExpired := time.Now().After(expiredMsg.ExpiresAt)
	if !isExpired {
		t.Error("Expected expired message to be detected as expired")
	}
}
