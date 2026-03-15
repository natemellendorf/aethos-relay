package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
)

func TestGossipV1HelloLoopbackBetweenRelays(t *testing.T) {
	relayA, _ := startRelayForTest(t, "relay-a", true, "", true)
	defer relayA.close()

	relayB, _ := startRelayForTest(t, "relay-b", true, "", true)
	defer relayB.close()

	observer := &helloObserver{counts: map[string]int{}}
	relayA.peerManager.SetEventObserver(observer.observe("relay-a"))
	relayB.peerManager.SetEventObserver(observer.observe("relay-b"))

	relayA.peerManager.AddPeerURL(relayB.fedURL)
	relayB.peerManager.AddPeerURL(relayA.fedURL)

	waitForCondition(t, 5*time.Second, func() bool {
		return relayA.peerManager.GetPeerCount() > 0 &&
			relayB.peerManager.GetPeerCount() > 0 &&
			observer.countFor("relay-a") > 0 &&
			observer.countFor("relay-b") > 0 &&
			len(relayA.peerManager.GetHealthyPeers()) > 0 &&
			len(relayB.peerManager.GetHealthyPeers()) > 0
	}, "relay hello loopback establishes healthy bidirectional sessions")

	if relayA.peerManager.GetPeerCount() == 0 {
		t.Fatal("relay-a never established a gossip session")
	}
	if relayB.peerManager.GetPeerCount() == 0 {
		t.Fatal("relay-b never established a gossip session")
	}
	if observer.countFor("relay-a") == 0 {
		t.Fatal("relay-a never validated a peer HELLO")
	}
	if observer.countFor("relay-b") == 0 {
		t.Fatal("relay-b never validated a peer HELLO")
	}
	if len(relayA.peerManager.GetHealthyPeers()) == 0 {
		t.Fatal("relay-a gossip session is not healthy")
	}
	if len(relayB.peerManager.GetHealthyPeers()) == 0 {
		t.Fatal("relay-b gossip session is not healthy")
	}

	assertNoObserverErrors(t, relayA.peerManager)
	assertNoObserverErrors(t, relayB.peerManager)
}

func TestGossipV1ClientEncounterFullFlow(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-client-flow", false, "", true)
	defer relay.close()

	conn := mustDial(t, wsURL)
	defer conn.Close()

	localHello := readEnvelope(t, conn)
	assertFrameType(t, localHello, gossipv1.FrameTypeHello)

	clientHello := gossipv1.BuildRelayHello("client-flow")
	writeEnvelope(t, conn, gossipv1.FrameTypeHello, clientHello)

	initialSummary := readEnvelope(t, conn)
	assertFrameType(t, initialSummary, gossipv1.FrameTypeSummary)

	now := time.Now().UTC()
	createdAt := now.Unix()
	expiresAt := now.Add(time.Hour).Unix()
	loopMsgID := testItemID("sender-1", clientHello.NodeID, "QQ", createdAt, expiresAt)
	writeEnvelope(t, conn, gossipv1.FrameTypeSummary, gossipv1.BuildSummaryPayload([]string{loopMsgID}))

	request := readEnvelope(t, conn)
	assertFrameType(t, request, gossipv1.FrameTypeRequest)

	writeEnvelope(t, conn, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: []gossipv1.TransferObject{{
		ID:         loopMsgID,
		From:       "sender-1",
		To:         clientHello.NodeID,
		PayloadB64: "QQ",
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
	}}})

	receipt := readEnvelope(t, conn)
	assertFrameType(t, receipt, gossipv1.FrameTypeReceipt)
	parsedReceipt, err := gossipv1.ParseReceiptPayload(receipt.Payload)
	if err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if len(parsedReceipt.Accepted) != 1 || parsedReceipt.Accepted[0] != loopMsgID {
		t.Fatalf("unexpected receipt payload: %#v", parsedReceipt)
	}

	writeEnvelope(t, conn, gossipv1.FrameTypeRequest, gossipv1.RequestPayload{Want: []string{loopMsgID}})

	transfer := readEnvelope(t, conn)
	assertFrameType(t, transfer, gossipv1.FrameTypeTransfer)
	parsedTransfer, err := gossipv1.ParseTransferPayloadMixed(transfer.Payload)
	if err != nil {
		t.Fatalf("parse transfer: %v", err)
	}
	if len(parsedTransfer.Objects) != 1 || parsedTransfer.Objects[0].Object.ID != loopMsgID {
		t.Fatalf("unexpected transfer payload: %#v", parsedTransfer)
	}

	writeEnvelope(t, conn, gossipv1.FrameTypeReceipt, gossipv1.ReceiptPayload{Accepted: []string{loopMsgID}})

	if _, err := relay.store.GetMessageByID(context.Background(), loopMsgID); err != nil {
		t.Fatalf("expected persisted loopback message: %v", err)
	}
}

type helloObserver struct {
	mu     sync.Mutex
	counts map[string]int
}

func (o *helloObserver) observe(label string) func(*federation.Peer, gossipv1.Event) error {
	return func(_ *federation.Peer, event gossipv1.Event) error {
		if event.Type != gossipv1.EventTypeHelloValidated {
			return nil
		}

		o.mu.Lock()
		o.counts[label]++
		o.mu.Unlock()
		return nil
	}
}

func (o *helloObserver) countFor(label string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counts[label]
}

func assertNoObserverErrors(t *testing.T, manager *federation.PeerManager) {
	t.Helper()

	for peerID := range manager.GetPeers() {
		if err := manager.PeerLastObserverError(peerID); err != nil {
			t.Fatalf("unexpected non-fatal observer error for %s: %v", peerID, err)
		}
	}
}
