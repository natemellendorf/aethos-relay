package federation

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

type handshakeObserver struct {
	hellos int
	last   gossipv1.Event
	err    error
}

func (o *handshakeObserver) Observe(_ *Peer, event gossipv1.Event) error {
	o.last = event
	if o.err != nil {
		return o.err
	}
	if event.Type == gossipv1.EventTypeHelloValidated {
		o.hellos++
	}
	return nil
}

func TestPeerHealthRecordFailure_Peering(t *testing.T) {
	health := &PeerHealth{}

	health.RecordFailure()
	if health.FailureCount != 1 {
		t.Fatalf("failure count mismatch: got %d", health.FailureCount)
	}
	if health.IsHealthy {
		t.Fatal("health should be unhealthy after failure")
	}
	if !health.IsBackingOff() {
		t.Fatal("peer should back off after failure")
	}
}

func TestPeerHealthRecordSuccess_Peering(t *testing.T) {
	health := &PeerHealth{FailureCount: 3, IsHealthy: false, BackoffUntil: time.Now().Add(time.Minute)}

	health.RecordSuccess()
	if health.FailureCount != 0 {
		t.Fatalf("failure count mismatch: got %d", health.FailureCount)
	}
	if !health.IsHealthy {
		t.Fatal("health should be healthy after success")
	}
	if health.IsBackingOff() {
		t.Fatal("peer should not back off after success")
	}
}

func TestDiff(t *testing.T) {
	got := diff([]string{"a", "b", "c"}, []string{"b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("diff mismatch: %#v", got)
	}
}

func TestProcessEventsObserverFailureIsNonFatal(t *testing.T) {
	pm := NewPeerManager("relay-a", nil, nil, time.Hour)
	observer := &handshakeObserver{err: errors.New("observer failed")}
	pm.SetEventObserver(observer.Observe)

	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	events := []gossipv1.Event{{Type: gossipv1.EventTypeHelloValidated, FrameType: gossipv1.FrameTypeHello, Hello: ptrHello(gossipv1.BuildRelayHello("relay-b"))}}
	if err := pm.processEvents(peer, adapter, events); err != nil {
		t.Fatalf("process events unexpectedly failed: %v", err)
	}
	if adapter.LastObserverError() == nil {
		t.Fatal("expected adapter to retain observer error")
	}
	if !peer.Health.IsHealthy {
		t.Fatal("hello event should mark peer healthy")
	}
}

func TestProcessEventsFatalTerminates(t *testing.T) {
	pm := NewPeerManager("relay-a", nil, nil, time.Hour)
	peer := &Peer{ID: "peer-1", Done: make(chan struct{})}
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	fatal := errors.New("fatal validation")
	err := pm.processEvents(peer, adapter, []gossipv1.Event{{Type: gossipv1.EventTypeFatal, Err: fatal}})
	if !errors.Is(err, fatal) {
		t.Fatalf("expected fatal error propagation, got %v", err)
	}
}

func TestSessionAdapterReportsUntrustedUnauthenticatedRelayIngest(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), false)

	relayIngestFrame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeRelayIngest, map[string]any{"item_ids": []string{"a"}})
	if err != nil {
		t.Fatalf("encode relay_ingest: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(relayIngestFrame)
	if err != nil {
		t.Fatalf("prefix relay_ingest: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeUntrustedRelay {
		t.Fatalf("expected untrusted relay_ingest event, got %#v", events)
	}
	if len(events[0].ItemIDs) != 1 || events[0].ItemIDs[0] != "a" {
		t.Fatalf("expected parsed item_ids in event, got %#v", events[0].ItemIDs)
	}
	if adapter.UntrustedRelayIngestCount() != 1 {
		t.Fatalf("expected unauth relay_ingest count 1, got %d", adapter.UntrustedRelayIngestCount())
	}
}

func TestSessionAdapterRejectsRelayIngestAboveItemLimit(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	itemIDs := make([]string, gossipv1.MaxRelayIngestItems+1)
	for i := range itemIDs {
		itemIDs[i] = "item"
	}

	relayIngestFrame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeRelayIngest, map[string]any{"item_ids": itemIDs})
	if err != nil {
		t.Fatalf("encode relay_ingest: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(relayIngestFrame)
	if err != nil {
		t.Fatalf("prefix relay_ingest: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeFatal {
		t.Fatalf("expected fatal relay_ingest limit event, got %#v", events)
	}
}

func TestSessionAdapterAcceptsAuthenticatedRelayIngest(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	relayIngestFrame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeRelayIngest, map[string]any{"item_ids": []string{"a", "b"}})
	if err != nil {
		t.Fatalf("encode relay_ingest: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(relayIngestFrame)
	if err != nil {
		t.Fatalf("prefix relay_ingest: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeRelayIngest {
		t.Fatalf("expected relay_ingest event, got %#v", events)
	}
	if len(events[0].ItemIDs) != 2 || events[0].ItemIDs[0] != "a" || events[0].ItemIDs[1] != "b" {
		t.Fatalf("expected parsed item_ids in event, got %#v", events[0].ItemIDs)
	}
}

func TestSessionAdapterRejectsVersionMismatch(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	mismatched := gossipv1.BuildRelayHello("relay-b")
	mismatched.Version = gossipv1.GossipVersion + 1
	frame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeHello, mismatched)
	if err != nil {
		t.Fatalf("encode mismatched hello: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix mismatched hello: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeFatal {
		t.Fatalf("expected fatal event, got %#v", events)
	}
	if !adapter.Terminated() {
		t.Fatal("adapter should terminate after version mismatch")
	}
}

func TestSessionAdapterRejectsUnknownFrameType(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	frame, err := gossipv1.EncodeEnvelope("UNKNOWN", map[string]any{"x": uint64(1)})
	if err != nil {
		t.Fatalf("encode unknown frame: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix unknown frame: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeFatal {
		t.Fatalf("expected fatal unknown frame event, got %#v", events)
	}
}

func TestSessionAdapterRejectsOversizeFrameLength(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, uint32(gossipv1.MaxFrameBytes+1)); err != nil {
		t.Fatalf("write frame length: %v", err)
	}

	events := adapter.PushInbound(buf.Bytes())
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeFatal {
		t.Fatalf("expected fatal oversize event, got %#v", events)
	}
}

func TestSessionAdapterAcceptsRequestWithEmptyWant(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)

	frame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeRequest, map[string]any{"want": []string{}})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix request: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeRequest {
		t.Fatalf("expected request event, got %#v", events)
	}
	if events[0].Request == nil || len(events[0].Request.Want) != 0 {
		t.Fatalf("expected empty want no-op payload, got %#v", events[0].Request)
	}
}

func TestSessionAdapterReceiptMustMatchPendingTransferIDs(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt([]string{"msg-1"})

	frame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeReceipt, map[string]any{
		"accepted": []string{"msg-unknown"},
	})
	if err != nil {
		t.Fatalf("encode receipt: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix receipt: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeFatal {
		t.Fatalf("expected fatal mismatched receipt event, got %#v", events)
	}
}

func TestSessionAdapterAllowsEmptyReceiptForEmptyTransfer(t *testing.T) {
	adapter := gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-a"), true)
	adapter.SetExpectedReceipt(nil)

	frame, err := gossipv1.EncodeEnvelope(gossipv1.FrameTypeReceipt, map[string]any{
		"accepted": []string{},
	})
	if err != nil {
		t.Fatalf("encode receipt: %v", err)
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("prefix receipt: %v", err)
	}

	events := adapter.PushInbound(prefixed)
	if len(events) != 1 || events[0].Type != gossipv1.EventTypeReceipt {
		t.Fatalf("expected receipt event, got %#v", events)
	}
}

func TestPeerManagerMetricsAndHealthHelpers(t *testing.T) {
	pm := NewPeerManager("relay-a", nil, model.NewClientRegistry(), time.Hour)
	peer := &Peer{
		ID:          "peer-1",
		URL:         "ws://example.test/federation/ws",
		ConnectedAt: time.Now().Add(-time.Minute),
		Health:      PeerHealth{LastSeen: time.Now(), IsHealthy: true},
		Done:        make(chan struct{}),
		Metrics:     model.NewPeerMetrics("peer-1"),
	}

	pm.addPeer(peer)
	if pm.GetPeerCount() != 1 {
		t.Fatalf("expected peer count 1, got %d", pm.GetPeerCount())
	}

	healthMap := pm.GetPeers()
	if len(healthMap) != 1 {
		t.Fatalf("expected one peer health entry, got %d", len(healthMap))
	}
	if !pm.IsPeerHealthy("peer-1") {
		t.Fatal("expected peer to be healthy")
	}

	healthy := pm.GetHealthyPeers()
	if len(healthy) != 1 || healthy[0] != "peer-1" {
		t.Fatalf("unexpected healthy peers: %#v", healthy)
	}

	metrics := pm.GetPeerMetrics()
	if len(metrics) != 1 || metrics[0].PeerID != "peer-1" {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}

	pm.removePeer(peer)
	if pm.GetPeerCount() != 0 {
		t.Fatalf("expected peer count 0, got %d", pm.GetPeerCount())
	}
}

func TestAnnounceMessageRequiresMessage(t *testing.T) {
	pm := NewPeerManager("relay-a", nil, nil, time.Hour)

	err := pm.AnnounceMessage(nil)
	if err == nil {
		t.Fatal("expected announce to fail with nil message")
	}
	if err.Error() != "federation: announce message is required" {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := pm.AnnounceMessage(&model.Message{ID: "msg-123"}); err != nil {
		t.Fatalf("expected announce with message to be no-op success without peers: %v", err)
	}
}

func TestForwardToPeersRequiresMessage(t *testing.T) {
	pm := NewPeerManager("relay-a", nil, nil, time.Hour)

	err := pm.ForwardToPeers(nil, "relay-origin")
	if err == nil {
		t.Fatal("expected forward to fail with nil message")
	}
	if err.Error() != "federation: forward message is required" {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := pm.ForwardToPeers(&model.Message{ID: "msg-123"}, "relay-origin"); err != nil {
		t.Fatalf("expected forward with message to be no-op success without peers: %v", err)
	}
}

func ptrHello(value gossipv1.HelloPayload) *gossipv1.HelloPayload {
	copy := value
	return &copy
}
