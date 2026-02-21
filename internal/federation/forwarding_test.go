package federation

import (
	"testing"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestForwardingStrategyTopK(t *testing.T) {
	config := &ForwardingConfig{
		TopK:        2,
		ExploreProb: 0.0, // Disable exploration for deterministic test
	}
	strategy := NewForwardingStrategy(config)

	// Create peer metrics with different scores
	peers := map[string]*model.PeerMetrics{
		"peer1": {PeerID: "peer1", Score: 0.9, Seed: 1},
		"peer2": {PeerID: "peer2", Score: 0.8, Seed: 2},
		"peer3": {PeerID: "peer3", Score: 0.7, Seed: 3},
		"peer4": {PeerID: "peer4", Score: 0.6, Seed: 4},
	}

	// Set them as healthy
	for _, p := range peers {
		p.Connected = true
	}

	selected := strategy.SelectPeers(peers, "")

	if len(selected) != 2 {
		t.Errorf("Expected 2 peers selected, got %d", len(selected))
	}

	// Should select top 2 by score
	if selected[0] != "peer1" {
		t.Errorf("Expected peer1 (highest score) to be selected, got %s", selected[0])
	}
	if selected[1] != "peer2" {
		t.Errorf("Expected peer2 (second highest) to be selected, got %s", selected[1])
	}
}

func TestForwardingStrategyExcludesOrigin(t *testing.T) {
	config := &ForwardingConfig{
		TopK:        3,
		ExploreProb: 0.0,
	}
	strategy := NewForwardingStrategy(config)

	peers := map[string]*model.PeerMetrics{
		"peer1": {PeerID: "peer1", Score: 0.9, Seed: 1},
		"peer2": {PeerID: "peer2", Score: 0.8, Seed: 2},
		"peer3": {PeerID: "peer3", Score: 0.7, Seed: 3},
	}

	for _, p := range peers {
		p.Connected = true
	}

	// Select with origin as peer1
	selected := strategy.SelectPeers(peers, "peer1")

	// Should not include origin
	for _, s := range selected {
		if s == "peer1" {
			t.Error("Origin peer should not be selected")
		}
	}
}

func TestForwardingStrategyExploration(t *testing.T) {
	// Test with exploration enabled
	// We use a fixed seed to make this deterministic
	config := &ForwardingConfig{
		TopK:        1,
		ExploreProb: 1.0, // Always explore
	}
	strategy := NewForwardingStrategy(config)

	peers := map[string]*model.PeerMetrics{
		"peer1": {PeerID: "peer1", Score: 0.9, Seed: 1},
		"peer2": {PeerID: "peer2", Score: 0.8, Seed: 2},
	}

	for _, p := range peers {
		p.Connected = true
	}

	// With exploration prob 1.0, should always include non-topK
	selected := strategy.SelectPeers(peers, "")

	if len(selected) != 2 {
		t.Errorf("Expected 2 peers with exploration, got %d", len(selected))
	}
}

func TestForwardingStrategyUnhealthyPeers(t *testing.T) {
	config := &ForwardingConfig{
		TopK:        3,
		ExploreProb: 0.0,
	}
	strategy := NewForwardingStrategy(config)

	peers := map[string]*model.PeerMetrics{
		"peer1": {PeerID: "peer1", Score: 0.9, Connected: true},
		"peer2": {PeerID: "peer2", Score: 0.8, Connected: true},
		"peer3": {PeerID: "peer3", Score: 0.1, Connected: false}, // Unhealthy
		"peer4": {PeerID: "peer4", Score: 0.7, Connected: true},
	}

	selected := strategy.SelectPeers(peers, "")

	// Should not include unhealthy peer
	for _, s := range selected {
		if s == "peer3" {
			t.Error("Unhealthy peer should not be selected")
		}
	}
}
