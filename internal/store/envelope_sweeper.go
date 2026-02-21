package store

import (
	"context"
	"log"
	"time"
)

// EnvelopeTTLConfig holds configuration for envelope TTL sweeping.
type EnvelopeTTLConfig struct {
	Interval  time.Duration
	MaxTTL    time.Duration
	BatchSize int
}

// EnvelopeTTLDefaultConfig returns default configuration for envelope TTL sweeping.
func EnvelopeTTLDefaultConfig(maxTTL time.Duration) *EnvelopeTTLConfig {
	return &EnvelopeTTLConfig{
		Interval:  30 * time.Second,
		MaxTTL:    maxTTL,
		BatchSize: 100,
	}
}

// EnvelopeSweeper removes expired envelopes from the store.
type EnvelopeSweeper struct {
	store   EnvelopeStore
	config  *EnvelopeTTLConfig
	stopCh  chan struct{}
	expired func()
}

// NewEnvelopeSweeper creates a new envelope sweeper.
func NewEnvelopeSweeper(store EnvelopeStore, config *EnvelopeTTLConfig) *EnvelopeSweeper {
	return &EnvelopeSweeper{
		store:   store,
		config:  config,
		stopCh:  make(chan struct{}),
		expired: func() {},
	}
}

// SetExpiredCounter sets the callback for expired envelopes.
func (s *EnvelopeSweeper) SetExpiredCounter(f func()) {
	s.expired = f
}

// Start starts the envelope sweeper.
func (s *EnvelopeSweeper) Start(ctx context.Context) {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep removes expired envelopes.
func (s *EnvelopeSweeper) sweep() {
	ctx := context.Background()
	now := time.Now()

	// Get expired envelopes
	expired, err := s.store.GetExpiredEnvelopes(ctx, now)
	if err != nil {
		log.Printf("envelope sweeper: failed to get expired envelopes: %v", err)
		return
	}

	if len(expired) == 0 {
		return
	}

	log.Printf("envelope sweeper: found %d expired envelopes", len(expired))

	// Remove expired envelopes
	for _, env := range expired {
		if err := s.store.RemoveEnvelope(ctx, env.ID); err != nil {
			log.Printf("envelope sweeper: failed to remove envelope %s: %v", env.ID, err)
			continue
		}
		s.expired()
	}

	// Update last sweep time
	if err := s.store.SetLastSweepTime(ctx, now); err != nil {
		log.Printf("envelope sweeper: failed to set last sweep time: %v", err)
	}
}

// GetLastSweepTime returns the last time the sweeper ran.
func (s *EnvelopeSweeper) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return s.store.GetLastSweepTime(ctx)
}

// Stop stops the envelope sweeper.
func (s *EnvelopeSweeper) Stop() error {
	close(s.stopCh)
	return nil
}
