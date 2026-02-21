package store

import (
	"context"
	"log"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
)

// DescriptorSweeper periodically removes expired descriptors.
type DescriptorSweeper struct {
	store    DescriptorStore
	interval time.Duration
	stopCh   chan struct{}
}

// NewDescriptorSweeper creates a new descriptor sweeper.
func NewDescriptorSweeper(store DescriptorStore, interval time.Duration) *DescriptorSweeper {
	return &DescriptorSweeper{
		store:    store,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start starts the background sweeper.
func (s *DescriptorSweeper) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run immediately on start
	s.sweep(ctx)

	for {
		select {
		case <-ticker.C:
			s.sweep(ctx)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the background sweeper.
func (s *DescriptorSweeper) Stop() {
	close(s.stopCh)
}

// sweep performs one sweep iteration.
func (s *DescriptorSweeper) sweep(ctx context.Context) {
	now := time.Now()
	expired, err := s.store.GetExpiredDescriptors(ctx, now)
	if err != nil {
		log.Printf("descriptor_sweeper: failed to get expired descriptors: %v", err)
		return
	}

	for _, desc := range expired {
		if err := s.store.DeleteDescriptor(ctx, desc.RelayID); err != nil {
			log.Printf("descriptor_sweeper: failed to remove descriptor %s: %v", desc.RelayID, err)
			continue
		}
		metrics.IncrementDescriptorsExpired()
		log.Printf("descriptor_sweeper: removed expired descriptor %s (expired at %d)", desc.RelayID, desc.ExpiresAt)
	}

	// Update registry size metric
	count, err := s.store.CountDescriptors(ctx)
	if err == nil {
		metrics.SetDescriptorsRegistrySize(count)
	}
}
