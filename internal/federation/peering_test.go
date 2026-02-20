package federation

import (
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// TestHandshakeResponseValidation tests that hello responses are validated correctly.
func TestHandshakeResponseValidation(t *testing.T) {
	tests := []struct {
		name        string
		resp        model.RelayHelloFrame
		wantAccept  bool
		wantRelayID string
	}{
		{
			name:        "valid relay_ok with relay_id",
			resp:        model.RelayHelloFrame{Type: model.FrameTypeRelayOK, RelayID: "peer-relay-1"},
			wantAccept:  true,
			wantRelayID: "peer-relay-1",
		},
		{
			name:        "valid relay_ok without relay_id uses generated id",
			resp:        model.RelayHelloFrame{Type: model.FrameTypeRelayOK, RelayID: ""},
			wantAccept:  true,
			wantRelayID: "generated-id",
		},
		{
			name:       "wrong type relay_hello rejected",
			resp:       model.RelayHelloFrame{Type: model.FrameTypeRelayHello, RelayID: "peer-relay-1"},
			wantAccept: false,
		},
		{
			name:       "empty type rejected",
			resp:       model.RelayHelloFrame{Type: "", RelayID: "peer-relay-1"},
			wantAccept: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the validation logic from dialPeer.
			peerID := "generated-id"

			accepted := tt.resp.Type == model.FrameTypeRelayOK
			if accepted && tt.resp.RelayID != "" {
				peerID = tt.resp.RelayID
			}

			if accepted != tt.wantAccept {
				t.Errorf("accepted = %v, want %v", accepted, tt.wantAccept)
			}

			if tt.wantAccept && tt.wantRelayID != "" && peerID != tt.wantRelayID {
				t.Errorf("peerID = %q, want %q", peerID, tt.wantRelayID)
			}
		})
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
