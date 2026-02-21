package store

import (
	"context"
	"log"
	"time"
)

// DescriptorSweeper periodically removes expired descriptors from the store.
type DescriptorSweeper struct {
	store      DescriptorStore
	interval   time.Duration
	maxTTL     time.Duration
	stopCh     chan struct{}
	expiredCtr func() // Metrics counter incrementer
}

// NewDescriptorSweeper creates a new descriptor sweeper.
func NewDescriptorSweeper(store DescriptorStore, interval, maxTTL time.Duration) *DescriptorSweeper {
	return &DescriptorSweeper{
		store:    store,
		interval: interval,
		maxTTL:   maxTTL,
		stopCh:   make(chan struct{}),
	}
}

// SetExpiredCounter sets the callback for expired descriptor counting.
func (s *DescriptorSweeper) SetExpiredCounter(f func()) {
	s.expiredCtr = f
}

// Start starts the background descriptor sweeper.
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

// Stop stops the background descriptor sweeper.
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

	removed := 0
	for _, desc := range expired {
		if err := s.store.RemoveDescriptor(ctx, desc.RelayID); err != nil {
			log.Printf("descriptor_sweeper: failed to remove descriptor %s: %v", desc.RelayID, err)
			continue
		}
		removed++
		if s.expiredCtr != nil {
			s.expiredCtr()
		}
		log.Printf("descriptor_sweeper: removed expired descriptor %s (expired at %v)", desc.RelayID, desc.ExpiresAt)
	}

	if removed > 0 {
		log.Printf("descriptor_sweeper: removed %d expired descriptors", removed)
	}
}
