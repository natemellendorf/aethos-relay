package gossipv1

import "testing"

func TestSessionAdapterReceiptValidationEmptyReceiptWithPendingTransferFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal empty receipt coverage event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationPartialReceiptWithPendingTransferFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1", "msg-2"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{"msg-1"},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal partial receipt coverage event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationRejectedEntryMissingIDAndIndexFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{},
		"rejected": []map[string]any{{
			"reason": "invalid transfer object",
		}},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal rejected coverage event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationEmptyTransferWithEmptyReceiptPasses(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt(nil)

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{},
	}))
	if len(events) != 1 || events[0].Type != EventTypeReceipt {
		t.Fatalf("expected receipt event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationUnknownAcceptedIDFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{"msg-unknown"},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal mismatched receipt event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationRejectedIndexResolvesCoveragePasses(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1", "msg-2"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{"msg-1"},
		"rejected": []map[string]any{{
			"index":  uint64(1),
			"reason": "invalid transfer object",
		}},
	}))
	if len(events) != 1 || events[0].Type != EventTypeReceipt {
		t.Fatalf("expected receipt event with accepted+rejected full coverage, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationDuplicateAcknowledgementFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1", "msg-2"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"accepted": []string{"msg-1"},
		"rejected": []map[string]any{{
			"id":     "msg-1",
			"reason": "duplicate ack",
		}, {
			"id":     "msg-2",
			"reason": "invalid transfer object",
		}},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal duplicate ack event, got %#v", events)
	}
}

func mustPrefixedEnvelope(t *testing.T, frameType string, payload any) []byte {
	t.Helper()

	frame, err := EncodeEnvelope(frameType, payload)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	prefixed, err := EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix envelope: %v", err)
	}

	return prefixed
}
