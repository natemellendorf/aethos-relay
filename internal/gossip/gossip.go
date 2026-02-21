package gossip

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

// PeerRateLimiter tracks descriptors received per peer.
type PeerRateLimiter struct {
	mu       sync.RWMutex
	peers    map[string]*peerLimit
	interval time.Duration
}

type peerLimit struct {
	count   int
	resetAt time.Time
}

// NewPeerRateLimiter creates a new peer rate limiter.
func NewPeerRateLimiter(interval time.Duration) *PeerRateLimiter {
	return &PeerRateLimiter{
		peers:    make(map[string]*peerLimit),
		interval: interval,
	}
}

// Allow checks if the peer is allowed to send more descriptors.
func (r *PeerRateLimiter) Allow(peerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	limit, exists := r.peers[peerID]

	if !exists || now.After(limit.resetAt) {
		// New window
		r.peers[peerID] = &peerLimit{
			count:   1,
			resetAt: now.Add(r.interval),
		}
		return true
	}

	if limit.count >= model.MAX_DESCRIPTORS_PER_PEER_PER_HOUR {
		return false
	}

	limit.count++
	return true
}

// Gossip handles relay descriptor gossip protocol.
type Gossip struct {
	store       store.DescriptorStore
	rateLimiter *PeerRateLimiter
	now         func() time.Time // For testing
}

// NewGossip creates a new gossip handler.
func NewGossip(store store.DescriptorStore) *Gossip {
	return &Gossip{
		store:       store,
		rateLimiter: NewPeerRateLimiter(time.Hour),
		now:         time.Now,
	}
}

// HandleDescriptors processes incoming relay descriptors from a peer.
func (g *Gossip) HandleDescriptors(ctx context.Context, peerID string, descriptors []model.RelayDescriptor) ([]model.RelayDescriptorAck, error) {
	metrics.IncrementDescriptorsReceived()

	// Apply rate limiting
	if !g.rateLimiter.Allow(peerID) {
		log.Printf("gossip: rate limit exceeded for peer %s", peerID)
		metrics.IncrementDescriptorsRejectedRateLimit()
		return []model.RelayDescriptorAck{
			{Status: "rejected", Reason: "rate limit exceeded"},
		}, nil
	}

	// Enforce max descriptors per message
	descCount := len(descriptors)
	if descCount > model.MAX_DESCRIPTORS_PER_MESSAGE {
		descriptors = descriptors[:model.MAX_DESCRIPTORS_PER_MESSAGE]
	}

	now := g.now()
	acks := make([]model.RelayDescriptorAck, 0, len(descriptors))

	// Get current registry size for cap enforcement
	registrySize, err := g.store.CountDescriptors(ctx)
	if err != nil {
		log.Printf("gossip: failed to count descriptors: %v", err)
	}

	for _, desc := range descriptors {
		// Validate descriptor
		result := model.ValidateDescriptor(&desc, now)
		if !result.Valid {
			log.Printf("gossip: descriptor validation failed for %s: %s", desc.RelayID, result.ErrorReason)
			metrics.IncrementDescriptorsRejectedValidation()
			acks = append(acks, model.RelayDescriptorAck{
				RelayID: desc.RelayID,
				Status:  "rejected",
				Reason:  result.ErrorReason,
			})
			continue
		}

		// Check registry cap
		if registrySize >= model.MAX_TOTAL_DESCRIPTORS {
			// Try to replace if this one has later expiry
			existing, err := g.store.GetDescriptor(ctx, desc.RelayID)
			if err != nil || existing == nil {
				// Registry full and not an update - reject
				log.Printf("gossip: registry full, rejecting descriptor %s", desc.RelayID)
				metrics.IncrementDescriptorsRejectedRateLimit()
				acks = append(acks, model.RelayDescriptorAck{
					RelayID: desc.RelayID,
					Status:  "rejected",
					Reason:  "registry full",
				})
				continue
			}
			// Existing found - will update
		}

		// Dedupe: if exists, update last_seen_at only (preserves first_seen_at)
		// PutDescriptor handles this automatically

		// Persist
		if err := g.store.PutDescriptor(ctx, result.Descriptor); err != nil {
			log.Printf("gossip: failed to persist descriptor %s: %v", desc.RelayID, err)
			acks = append(acks, model.RelayDescriptorAck{
				RelayID: desc.RelayID,
				Status:  "rejected",
				Reason:  "storage error",
			})
			continue
		}

		metrics.IncrementDescriptorsAccepted()
		registrySize++

		acks = append(acks, model.RelayDescriptorAck{
			RelayID: desc.RelayID,
			Status:  "accepted",
		})
	}

	// Update registry size metric
	metrics.SetDescriptorsRegistrySize(registrySize)

	return acks, nil
}

// SelectSample selects a bounded sample of descriptors for gossip.
// Favor fresh, active, non-expiring descriptors.
func (g *Gossip) SelectSample(ctx context.Context) ([]model.RelayDescriptor, error) {
	all, err := g.store.GetAllDescriptors(ctx)
	if err != nil {
		return nil, err
	}

	if len(all) == 0 {
		return nil, nil
	}

	now := g.now()

	// Score descriptors: prefer fresh, active, non-expiring
	type scoredDesc struct {
		desc  *model.RelayDescriptor
		score float64
	}

	scored := make([]scoredDesc, 0, len(all))
	for _, d := range all {
		if d.IsExpired(now) {
			continue // Skip expired
		}

		score := float64(d.LastSeenAt)

		// Boost score for non-expiring (expires far in future)
		expiryBoost := float64(d.ExpiresAt - now.Unix())
		score += expiryBoost * 0.001 // Small boost for longer TTL

		scored = append(scored, scoredDesc{desc: d, score: score})
	}

	if len(scored) == 0 {
		return nil, nil
	}

	// Sort by score descending (most desirable first)
	// Using simple sort - for larger sets, consider heap
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Take top N
	sampleSize := model.GOSSIP_SAMPLE_SIZE
	if len(scored) < sampleSize {
		sampleSize = len(scored)
	}

	result := make([]model.RelayDescriptor, sampleSize)
	for i := 0; i < sampleSize; i++ {
		result[i] = *scored[i].desc
	}

	// Shuffle for network diversity
	rand.Shuffle(len(result), func(i, j int) {
		result[i], result[j] = result[j], result[i]
	})

	return result, nil
}
