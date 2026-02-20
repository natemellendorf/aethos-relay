package store

import (
	"context"
	"log"
	"time"
)

// TTLSweeper periodically removes expired messages from the store.
type TTLSweeper struct {
	store      Store
	interval   time.Duration
	maxTTL     time.Duration
	stopCh     chan struct{}
	expiredCtr func() // Metrics counter incrementer
}

// NewTTLSweeper creates a new TTL sweeper.
func NewTTLSweeper(store Store, interval, maxTTL time.Duration) *TTLSweeper {
	return &TTLSweeper{
		store:    store,
		interval: interval,
		maxTTL:   maxTTL,
		stopCh:   make(chan struct{}),
	}
}

// SetExpiredCounter sets the callback for expired message counting.
func (s *TTLSweeper) SetExpiredCounter(f func()) {
	s.expiredCtr = f
}

// Start starts the background sweeper.
func (s *TTLSweeper) Start(ctx context.Context) {
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
func (s *TTLSweeper) Stop() {
	close(s.stopCh)
}

// sweep performs one sweep iteration.
func (s *TTLSweeper) sweep(ctx context.Context) {
	now := time.Now()
	expired, err := s.store.GetExpiredMessages(ctx, now)
	if err != nil {
		log.Printf("sweeper: failed to get expired messages: %v", err)
		return
	}

	for _, msg := range expired {
		if err := s.store.RemoveMessage(ctx, msg.ID); err != nil {
			log.Printf("sweeper: failed to remove message %s: %v", msg.ID, err)
			continue
		}
		if s.expiredCtr != nil {
			s.expiredCtr()
		}
		log.Printf("sweeper: removed expired message %s (expired at %v)", msg.ID, msg.ExpiresAt)
	}

	// Update last sweep time
	if err := s.store.SetLastSweepTime(ctx, now); err != nil {
		log.Printf("sweeper: failed to set last sweep time: %v", err)
	}
}

// GetLastSweepTime returns the last time the sweeper ran.
func (s *TTLSweeper) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return s.store.GetLastSweepTime(ctx)
}
