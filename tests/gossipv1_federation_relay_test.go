package tests

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestGossipV1RelayToRelayFullEncounterReplicatesMessage(t *testing.T) {
	relayA, _ := startRelayForTest(t, "relay-phase4-a", true, "", true)
	defer relayA.close()
	relayB, _ := startRelayForTest(t, "relay-phase4-b", true, "", true)
	defer relayB.close()

	recorderA := newFederationEventRecorder()
	recorderB := newFederationEventRecorder()
	relayA.peerManager.SetEventObserver(recorderA.observe)
	relayB.peerManager.SetEventObserver(recorderB.observe)

	now := time.Now().UTC()
	msg := &model.Message{
		ID:        "phase4-r2r-flow-msg-1",
		From:      "sender-a",
		To:        "wayfarer-flow",
		Payload:   "QQ",
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := relayA.store.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist source message: %v", err)
	}

	relayA.peerManager.AddPeerURL(relayB.fedURL)

	waitForMessage(t, relayB.store, msg.ID)
	waitForCondition(t, 5*time.Second, func() bool {
		return relayA.peerManager.GetPeerCount() > 0 && relayB.peerManager.GetPeerCount() > 0
	}, "peer managers establish sessions")

	required := []gossipv1.EventType{
		gossipv1.EventTypeHelloValidated,
		gossipv1.EventTypeSummary,
		gossipv1.EventTypeRequest,
		gossipv1.EventTypeTransfer,
		gossipv1.EventTypeReceipt,
	}
	waitForCondition(t, 5*time.Second, func() bool {
		eventCounts := mergeEventCounts(recorderA.snapshotCounts(), recorderB.snapshotCounts())
		for _, eventType := range required {
			if eventCounts[eventType] == 0 {
				return false
			}
		}
		return true
	}, "relays observe full hello-summary-request-transfer-receipt encounter")

	waitForCondition(t, 5*time.Second, func() bool {
		return recorderA.hasAcceptedID(msg.ID)
	}, "origin relay observes receipt ack for replicated message")
}

func TestGossipV1RelayToRelayDuplicatesAndRepeatedEncountersConverge(t *testing.T) {
	relayA, _ := startRelayForTest(t, "relay-phase4-converge-a", true, "", true)
	defer relayA.close()
	relayB, _ := startRelayForTest(t, "relay-phase4-converge-b", true, "", true)
	defer relayB.close()

	now := time.Now().UTC()
	msg := &model.Message{
		ID:        "phase4-converge-msg-1",
		From:      "sender-a",
		To:        "wayfarer-converge",
		Payload:   "QQ",
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := relayA.store.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist source message: %v", err)
	}

	relayA.peerManager.AddPeerURL(relayB.fedURL)

	waitForMessage(t, relayB.store, msg.ID)
	originNodeID := gossipv1.BuildRelayHello("relay-phase4-converge-a").NodeID

	for i := 0; i < 3; i++ {
		if err := relayA.peerManager.AnnounceMessage(msg); err != nil {
			t.Fatalf("announce repeated summary iteration=%d: %v", i, err)
		}
		if err := relayB.peerManager.ForwardToPeers(msg, originNodeID); err != nil {
			t.Fatalf("forward repeated summary iteration=%d: %v", i, err)
		}
	}

	waitForCondition(t, 5*time.Second, func() bool {
		return hasExactlyQueuedIDs(relayA, msg.To, msg.ID) && hasExactlyQueuedIDs(relayB, msg.To, msg.ID)
	}, "both relays converge on identical single message state")
}

func TestGossipV1RelayToRelayInvalidObjectDoesNotPoisonValidState(t *testing.T) {
	relayA, _ := startRelayForTest(t, "relay-phase4-invalid-a", true, "", true)
	defer relayA.close()
	relayB, _ := startRelayForTest(t, "relay-phase4-invalid-b", true, "", true)
	defer relayB.close()

	recorderA := newFederationEventRecorder()
	relayA.peerManager.SetEventObserver(recorderA.observe)

	now := time.Now().UTC()
	valid := &model.Message{
		ID:        "phase4-invalid-valid-1",
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   "QQ",
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	invalid := &model.Message{
		ID:        "phase4-invalid-bad-1",
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   "%%%not-base64%%%",
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := relayA.store.PersistMessage(context.Background(), valid); err != nil {
		t.Fatalf("persist valid source message: %v", err)
	}
	if err := relayA.store.PersistMessage(context.Background(), invalid); err != nil {
		t.Fatalf("persist invalid source message: %v", err)
	}

	relayA.peerManager.AddPeerURL(relayB.fedURL)

	waitForMessage(t, relayB.store, valid.ID)
	if _, err := relayB.store.GetMessageByID(context.Background(), invalid.ID); err == nil {
		t.Fatalf("expected invalid payload message %s to be rejected", invalid.ID)
	}

	validAfterInvalid := &model.Message{
		ID:        "phase4-invalid-valid-2",
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   "Qg",
		CreatedAt: now.Add(time.Second),
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := relayA.store.PersistMessage(context.Background(), validAfterInvalid); err != nil {
		t.Fatalf("persist second valid message: %v", err)
	}
	if err := relayA.peerManager.AnnounceMessage(validAfterInvalid); err != nil {
		t.Fatalf("announce second valid message: %v", err)
	}

	waitForMessage(t, relayB.store, validAfterInvalid.ID)
	waitForCondition(t, 5*time.Second, func() bool {
		return recorderA.hasRejectedID(invalid.ID)
	}, "source relay observes receipt rejection for invalid object")
}

func TestGossipV1RelayToRelayForwardToPeersSuppressesOriginNode(t *testing.T) {
	relayA, _ := startRelayForTest(t, "relay-phase4-origin-a", true, "", true)
	defer relayA.close()
	relayB, _ := startRelayForTest(t, "relay-phase4-origin-b", true, "", true)
	defer relayB.close()

	recorderA := newFederationEventRecorder()
	relayA.peerManager.SetEventObserver(recorderA.observe)

	relayA.peerManager.AddPeerURL(relayB.fedURL)

	waitForCondition(t, 5*time.Second, func() bool {
		return relayA.peerManager.GetPeerCount() > 0 && relayB.peerManager.GetPeerCount() > 0
	}, "peer managers establish sessions")

	originNodeID := gossipv1.BuildRelayHello("relay-phase4-origin-a").NodeID
	sentinelID := "phase4-origin-suppression-msg-1"
	if err := relayB.peerManager.ForwardToPeers(&model.Message{ID: sentinelID}, originNodeID); err != nil {
		t.Fatalf("forward summary with origin suppression: %v", err)
	}

	waitForConditionToStayFalse(t, 400*time.Millisecond, func() bool {
		return recorderA.hasSummaryID(sentinelID)
	}, "origin relay receives forwarded summary for suppressed origin node")
}

func TestGossipV1FederationEndpointAcceptsClientLikePeerFrames(t *testing.T) {
	relay, _ := startRelayForTest(t, "relay-phase4-cross-role", true, "", true)
	defer relay.close()

	conn := mustDial(t, relay.fedURL)
	defer conn.Close()

	writeEnvelope(t, conn, gossipv1.FrameTypeHello, gossipv1.BuildRelayHello("client-like-federation-peer"))

	remoteHello := readEnvelope(t, conn)
	assertFrameType(t, remoteHello, gossipv1.FrameTypeHello)

	initialSummary := readEnvelope(t, conn)
	assertFrameType(t, initialSummary, gossipv1.FrameTypeSummary)

	writeEnvelope(t, conn, gossipv1.FrameTypeSummary, map[string]any{"have": []string{"phase4-cross-role-msg-1"}})

	request := readEnvelope(t, conn)
	assertFrameType(t, request, gossipv1.FrameTypeRequest)

	now := time.Now().UTC()
	writeEnvelope(t, conn, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: []gossipv1.TransferObject{{
		ID:         "phase4-cross-role-msg-1",
		From:       "sender-cross-role",
		To:         "wayfarer-cross-role",
		PayloadB64: "QQ",
		CreatedAt:  now.Unix(),
		ExpiresAt:  now.Add(time.Hour).Unix(),
	}}})

	receipt := readEnvelope(t, conn)
	assertFrameType(t, receipt, gossipv1.FrameTypeReceipt)
	parsedReceipt, err := gossipv1.ParseReceiptPayload(receipt.Payload)
	if err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if len(parsedReceipt.Accepted) != 1 || parsedReceipt.Accepted[0] != "phase4-cross-role-msg-1" {
		t.Fatalf("unexpected receipt payload: %#v", parsedReceipt)
	}

	if _, err := relay.store.GetMessageByID(context.Background(), "phase4-cross-role-msg-1"); err != nil {
		t.Fatalf("expected persisted cross-role message: %v", err)
	}
}

type federationEventRecorder struct {
	mu        sync.Mutex
	counts    map[gossipv1.EventType]int
	receipts  []gossipv1.ReceiptPayload
	summaries []gossipv1.SummaryPayload
}

func newFederationEventRecorder() *federationEventRecorder {
	return &federationEventRecorder{counts: make(map[gossipv1.EventType]int)}
}

func (r *federationEventRecorder) observe(_ *federation.Peer, event gossipv1.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.counts[event.Type]++
	if event.Type == gossipv1.EventTypeReceipt && event.Receipt != nil {
		r.receipts = append(r.receipts, *event.Receipt)
	}
	if event.Type == gossipv1.EventTypeSummary && event.Summary != nil {
		r.summaries = append(r.summaries, *event.Summary)
	}

	return nil
}

func (r *federationEventRecorder) snapshotCounts() map[gossipv1.EventType]int {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[gossipv1.EventType]int, len(r.counts))
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}

func (r *federationEventRecorder) hasRejectedID(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, receipt := range r.receipts {
		for _, rejected := range receipt.Rejected {
			if rejected.ID == id {
				return true
			}
		}
	}

	return false
}

func (r *federationEventRecorder) hasAcceptedID(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, receipt := range r.receipts {
		for _, accepted := range receipt.Accepted {
			if accepted == id {
				return true
			}
		}
	}

	return false
}

func (r *federationEventRecorder) hasSummaryID(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, summary := range r.summaries {
		for _, haveID := range summary.Have {
			if haveID == id {
				return true
			}
		}
	}

	return false
}

func mergeEventCounts(countSets ...map[gossipv1.EventType]int) map[gossipv1.EventType]int {
	out := make(map[gossipv1.EventType]int)
	for _, countSet := range countSets {
		for eventType, count := range countSet {
			out[eventType] += count
		}
	}
	return out
}

func waitForMessage(t *testing.T, st interface {
	GetMessageByID(ctx context.Context, msgID string) (*model.Message, error)
}, msgID string) {
	t.Helper()

	waitForCondition(t, 5*time.Second, func() bool {
		_, err := st.GetMessageByID(context.Background(), msgID)
		return err == nil
	}, "message replicated: "+msgID)
}

func hasExactlyQueuedIDs(relay *relayHarness, recipient string, expectedIDs ...string) bool {
	ids, err := relay.store.GetAllQueuedMessageIDs(context.Background(), recipient)
	if err != nil {
		return false
	}

	got := uniqueSorted(ids)
	want := uniqueSorted(expectedIDs)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
