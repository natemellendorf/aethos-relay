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
	msgPayload := "QQ"
	msg := &model.Message{
		ID:        testItemID("sender-a", "wayfarer-flow", msgPayload, now.Unix(), now.Add(30*time.Minute).Unix()),
		From:      "sender-a",
		To:        "wayfarer-flow",
		Payload:   msgPayload,
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	persistRelayMessageWithEnvelope(t, relayA, msg)

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
	msgPayload := "QQ"
	msg := &model.Message{
		ID:        testItemID("sender-a", "wayfarer-converge", msgPayload, now.Unix(), now.Add(30*time.Minute).Unix()),
		From:      "sender-a",
		To:        "wayfarer-converge",
		Payload:   msgPayload,
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	persistRelayMessageWithEnvelope(t, relayA, msg)

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
	validPayload := "QQ"
	valid := &model.Message{
		ID:        testItemID("sender-a", "wayfarer-invalid", validPayload, now.Unix(), now.Add(30*time.Minute).Unix()),
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   validPayload,
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	invalidPayload := "%%%not-base64%%%"
	invalid := &model.Message{
		ID:        testItemID("sender-a", "wayfarer-invalid", invalidPayload, now.Unix(), now.Add(30*time.Minute).Unix()),
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   invalidPayload,
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	persistRelayMessageWithEnvelope(t, relayA, valid)
	persistRelayMessageWithEnvelope(t, relayA, invalid)

	relayA.peerManager.AddPeerURL(relayB.fedURL)

	waitForMessage(t, relayB.store, valid.ID)
	if _, err := relayB.store.GetMessageByID(context.Background(), invalid.ID); err == nil {
		t.Fatalf("expected invalid payload message %s to be rejected", invalid.ID)
	}

	secondPayload := "Qg"
	validAfterInvalid := &model.Message{
		ID:        testItemID("sender-a", "wayfarer-invalid", secondPayload, now.Add(time.Second).Unix(), now.Add(30*time.Minute).Unix()),
		From:      "sender-a",
		To:        "wayfarer-invalid",
		Payload:   secondPayload,
		CreatedAt: now.Add(time.Second),
		ExpiresAt: now.Add(30 * time.Minute),
	}
	persistRelayMessageWithEnvelope(t, relayA, validAfterInvalid)
	if err := relayA.peerManager.AnnounceMessage(validAfterInvalid); err != nil {
		t.Fatalf("announce second valid message: %v", err)
	}

	waitForMessage(t, relayB.store, validAfterInvalid.ID)
	waitForCondition(t, 5*time.Second, func() bool {
		for peerID := range relayA.peerManager.GetPeers() {
			if relayA.peerManager.PeerLastObserverError(peerID) != nil {
				return false
			}
		}
		return true
	}, "source relay remains healthy after invalid object rejection")
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
	sentinelID := testItemID("sender-origin", "wayfarer-origin", "QQ", time.Now().UTC().Unix(), time.Now().UTC().Add(time.Hour).Unix())
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

	now := time.Now().UTC()
	createdAt := now.Unix()
	expiresAt := now.Add(time.Hour).Unix()
	itemID := testItemID("sender-cross-role", "wayfarer-cross-role", "QQ", createdAt, expiresAt)
	writeEnvelope(t, conn, gossipv1.FrameTypeSummary, gossipv1.BuildSummaryPayload([]string{itemID}))

	request := readEnvelope(t, conn)
	assertFrameType(t, request, gossipv1.FrameTypeRequest)

	writeEnvelope(t, conn, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: []gossipv1.TransferObject{
		mustTransferObject(t, itemID, "sender-cross-role", "wayfarer-cross-role", "QQ", createdAt, expiresAt),
	}})

	receipt := readEnvelope(t, conn)
	assertFrameType(t, receipt, gossipv1.FrameTypeReceipt)
	parsedReceipt, err := gossipv1.ParseReceiptPayload(receipt.Payload)
	if err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if len(parsedReceipt.Accepted) != 1 || parsedReceipt.Accepted[0] != itemID {
		t.Fatalf("unexpected receipt payload: %#v", parsedReceipt)
	}

	if _, err := relay.store.GetMessageByID(context.Background(), itemID); err != nil {
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
		for _, haveID := range summary.PreviewItemIDs {
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

func persistRelayMessageWithEnvelope(t *testing.T, relay *relayHarness, msg *model.Message) {
	t.Helper()

	if err := relay.store.PersistMessage(context.Background(), msg); err != nil {
		t.Fatalf("persist relay message: %v", err)
	}
	if relay.envelope == nil || msg == nil {
		return
	}
	if err := relay.envelope.PersistEnvelope(context.Background(), &model.Envelope{
		ID:            msg.ID,
		DestinationID: msg.To,
		OpaquePayload: []byte(msg.Payload),
		OriginRelayID: "",
		CreatedAt:     msg.CreatedAt,
		ExpiresAt:     msg.ExpiresAt,
	}); err != nil {
		t.Fatalf("persist relay envelope: %v", err)
	}
}
