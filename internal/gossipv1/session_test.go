package gossipv1

import "testing"

func TestSessionAdapterReceiptValidationEmptyReceiptWithPendingTransferPasses(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{},
	}))
	if len(events) != 1 || events[0].Type != EventTypeReceipt {
		t.Fatalf("expected receipt event for empty subset acknowledgement, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationPartialReceiptWithPendingTransferPasses(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1", "msg-2"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{"msg-1"},
	}))
	if len(events) != 1 || events[0].Type != EventTypeReceipt {
		t.Fatalf("expected receipt event for partial subset acknowledgement, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationLegacyRejectedKeyFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{},
		"rejected": []string{"legacy"},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal legacy rejected payload event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationEmptyTransferWithEmptyReceiptPasses(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt(nil)

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{},
	}))
	if len(events) != 1 || events[0].Type != EventTypeReceipt {
		t.Fatalf("expected receipt event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationUnknownAcceptedIDFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{"msg-unknown"},
	}))
	if len(events) != 1 || events[0].Type != EventTypeFatal {
		t.Fatalf("expected fatal mismatched receipt event, got %#v", events)
	}
}

func TestSessionAdapterReceiptValidationDuplicateAcknowledgementFails(t *testing.T) {
	adapter := NewSessionAdapter(BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1", "msg-2"})

	events := adapter.PushInbound(mustPrefixedEnvelope(t, FrameTypeReceipt, map[string]any{
		"received": []string{"msg-1", "msg-1"},
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
