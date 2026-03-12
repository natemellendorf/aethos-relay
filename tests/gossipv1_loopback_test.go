package tests

import (
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

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if relayA.peerManager.GetPeerCount() > 0 &&
			relayB.peerManager.GetPeerCount() > 0 &&
			observer.countFor("relay-a") > 0 &&
			observer.countFor("relay-b") > 0 &&
			len(relayA.peerManager.GetHealthyPeers()) > 0 &&
			len(relayB.peerManager.GetHealthyPeers()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

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
