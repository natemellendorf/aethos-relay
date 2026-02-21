package gossip

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	// GossipInterval is how often to gossip descriptors to peers.
	GossipInterval = 30 * time.Second

	// GossipSampleSize is the maximum number of descriptors to share per gossip round.
	GossipSampleSize = 50

	// StalenessThreshold marks descriptors as stale after this duration.
	StalenessThreshold = 1 * time.Hour
)

// PeerDescriptorTracker tracks descriptors received from each peer for rate limiting.
type PeerDescriptorTracker struct {
	store store.DescriptorStore
}

// NewPeerDescriptorTracker creates a new peer descriptor tracker.
func NewPeerDescriptorTracker(store store.DescriptorStore) *PeerDescriptorTracker {
	return &PeerDescriptorTracker{store: store}
}

// IsRateLimited checks if a peer has exceeded their descriptor rate limit.
func (t *PeerDescriptorTracker) IsRateLimited(ctx context.Context, peerID string) bool {
	count, err := t.store.GetPeerDescriptorCount(ctx, peerID)
	if err != nil {
		log.Printf("gossip: failed to get peer descriptor count: %v", err)
		return true // Fail safe: rate limit on error
	}
	return count >= model.MAX_DESCRIPTORS_PER_PEER_PER_HOUR
}

// GossipEngine handles descriptor gossip between relays.
type GossipEngine struct {
	descriptorStore store.DescriptorStore
	relayID         string
	enabled         bool
	tracker         *PeerDescriptorTracker
	stopCh          chan struct{}
}

// NewGossipEngine creates a new gossip engine.
func NewGossipEngine(descriptorStore store.DescriptorStore, relayID string, enabled bool) *GossipEngine {
	return &GossipEngine{
		descriptorStore: descriptorStore,
		relayID:         relayID,
		enabled:         enabled,
		tracker:         NewPeerDescriptorTracker(descriptorStore),
		stopCh:          make(chan struct{}),
	}
}

// Run starts the gossip engine.
func (g *GossipEngine) Run(ctx context.Context) {
	if !g.enabled {
		log.Println("gossip: disabled, skipping gossip loop")
		return
	}

	ticker := time.NewTicker(GossipInterval)
	defer ticker.Stop()

	// Run immediately on start
	g.gossipDescriptors(ctx)

	for {
		select {
		case <-ticker.C:
			g.gossipDescriptors(ctx)
		case <-g.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the gossip engine.
func (g *GossipEngine) Stop() {
	close(g.stopCh)
}

// gossipDescriptors performs one gossip round.
func (g *GossipEngine) gossipDescriptors(ctx context.Context) {
	// Get all valid descriptors
	descriptors, err := g.descriptorStore.GetAllDescriptors(ctx)
	if err != nil {
		log.Printf("gossip: failed to get descriptors: %v", err)
		return
	}

	if len(descriptors) == 0 {
		return
	}

	// Select sample using the gossip algorithm
	sample := g.selectSample(descriptors, GossipSampleSize)

	if len(sample) == 0 {
		return
	}

	// Update metrics
	total, _ := g.descriptorStore.CountDescriptors(ctx)
	metrics.SetDescriptorsRegistrySize(total)

	log.Printf("gossip: selected %d descriptors for gossip (total: %d)", len(sample), total)

	// The actual sending to peers is handled by the federation layer
	// This function returns the selected descriptors for the federation layer to send
}

// selectSample selects descriptors to share with peers.
// The algorithm favors fresh descriptors but includes some older ones for coverage.
func (g *GossipEngine) selectSample(descriptors []*model.RelayDescriptor, maxSample int) []*model.RelayDescriptor {
	if len(descriptors) == 0 {
		return nil
	}
	if len(descriptors) <= maxSample {
		return descriptors
	}

	// Sort by freshness (most recently seen first)
	sorted := make([]*model.RelayDescriptor, len(descriptors))
	copy(sorted, descriptors)

	// Simple bubble sort for freshness (most recent first)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].LastSeenAt.After(sorted[i].LastSeenAt) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Strategy: Include fresh descriptors (70%) + random older descriptors (30%)
	freshCount := int(float64(maxSample) * 0.7)
	if freshCount > len(sorted) {
		freshCount = len(sorted)
	}

	// Take top freshCount descriptors
	sample := make([]*model.RelayDescriptor, 0, maxSample)
	sample = append(sample, sorted[:freshCount]...)

	// Add random selection from remaining
	remaining := sorted[freshCount:]
	if len(remaining) > 0 {
		rand.Shuffle(len(remaining), func(i, j int) {
			remaining[i], remaining[j] = remaining[j], remaining[i]
		})
		needed := maxSample - freshCount
		if needed > len(remaining) {
			needed = len(remaining)
		}
		sample = append(sample, remaining[:needed]...)
	}

	return sample
}

// SelectBoundedSample selects a bounded sample of descriptors.
// This is exported for testing.
func (g *GossipEngine) SelectBoundedSample(descriptors []*model.RelayDescriptor, maxSample int) []*model.RelayDescriptor {
	return g.selectSample(descriptors, maxSample)
}

// FilterStale removes stale descriptors from the list.
func FilterStale(descriptors []*model.RelayDescriptor, threshold time.Duration) []*model.RelayDescriptor {
	var result []*model.RelayDescriptor
	for _, d := range descriptors {
		if !d.IsStale(threshold) {
			result = append(result, d)
		}
	}
	return result
}

// EnforceRegistryCap ensures the descriptor registry doesn't exceed the limit.
// Returns descriptors that should be removed (oldest first).
func EnforceRegistryCap(descriptors []*model.RelayDescriptor, maxDescriptors int) []*model.RelayDescriptor {
	if len(descriptors) <= maxDescriptors {
		return nil
	}

	// Sort by last_seen_at ascending (oldest first)
	sorted := make([]*model.RelayDescriptor, len(descriptors))
	copy(sorted, descriptors)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].LastSeenAt.Before(sorted[i].LastSeenAt) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Return oldest descriptors to remove
	removeCount := len(descriptors) - maxDescriptors
	return sorted[:removeCount]
}

// GetStaleDescriptors returns descriptors that haven't been seen recently.
func GetStaleDescriptors(descriptors []*model.RelayDescriptor, threshold time.Duration) []*model.RelayDescriptor {
	var stale []*model.RelayDescriptor
	for _, d := range descriptors {
		if d.IsStale(threshold) {
			stale = append(stale, d)
		}
	}
	return stale
}
