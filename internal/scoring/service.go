package scoring

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

// RelayScoreStore defines the interface for persisting relay scores.
type RelayScoreStore interface {
	// GetScore retrieves a relay's score.
	GetScore(ctx context.Context, relayID string) (*model.RelayScore, error)

	// PutScore stores or updates a relay's score.
	PutScore(ctx context.Context, score *model.RelayScore) error

	// GetAllScores returns all relay scores.
	GetAllScores(ctx context.Context) ([]*model.RelayScore, error)

	// RemoveScore removes a relay's score.
	RemoveScore(ctx context.Context, relayID string) error
}

// ScoringService manages relay scoring and adaptive publish width.
type ScoringService struct {
	scores    map[string]*model.RelayScore
	scoresMu  sync.RWMutex
	selector  *model.RelaySelector
	store     RelayScoreStore
	decayTick *time.Ticker
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewScoringService creates a new scoring service.
func NewScoringService(store RelayScoreStore) *ScoringService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScoringService{
		scores:   make(map[string]*model.RelayScore),
		selector: model.NewRelaySelector(),
		store:    store,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start starts the scoring service with periodic decay.
func (s *ScoringService) Start() {
	// Apply decay every hour
	s.decayTick = time.NewTicker(time.Hour)
	go func() {
		for {
			select {
			case <-s.ctx.Done():
				s.decayTick.Stop()
				return
			case <-s.decayTick.C:
				s.applyDecay()
			}
		}
	}()

	// Load existing scores from store
	if s.store != nil {
		go s.loadScores()
	}

	// Update metrics
	metrics.SetPublishWidth(s.selector.CurrentWidth())
	log.Println("scoring: service started")
}

// Stop stops the scoring service.
func (s *ScoringService) Stop() {
	s.cancel()
	log.Println("scoring: service stopped")
}

// loadScores loads scores from the persistent store.
func (s *ScoringService) loadScores() {
	if s.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	scores, err := s.store.GetAllScores(ctx)
	if err != nil {
		log.Printf("scoring: failed to load scores: %v", err)
		return
	}

	s.scoresMu.Lock()
	defer s.scoresMu.Unlock()

	for _, score := range scores {
		s.scores[score.RelayID] = score
		metrics.SetRelayScore(score.RelayID, score.Score)
	}

	log.Printf("scoring: loaded %d scores from store", len(scores))
}

// applyDecay applies time-based decay to all scores.
func (s *ScoringService) applyDecay() {
	s.scoresMu.Lock()
	defer s.scoresMu.Unlock()

	for _, score := range s.scores {
		score.ApplyDecay()
		metrics.SetRelayScore(score.RelayID, score.Score)

		// Persist updated score
		if s.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.store.PutScore(ctx, score); err != nil {
				log.Printf("scoring: failed to persist score decay: %v", err)
			}
			cancel()
		}
	}
}

// GetOrCreateScore gets an existing score or creates a new one.
func (s *ScoringService) GetOrCreateScore(relayID string) *model.RelayScore {
	s.scoresMu.Lock()
	defer s.scoresMu.Unlock()

	if score, ok := s.scores[relayID]; ok {
		return score
	}

	score := model.NewRelayScore(relayID)
	s.scores[relayID] = score
	metrics.SetRelayScore(relayID, score.Score)

	return score
}

// RecordPublishSuccess records a successful publish to a relay.
func (s *ScoringService) RecordPublishSuccess(relayID string, latencyMs float64) {
	score := s.GetOrCreateScore(relayID)
	score.RecordSuccess(latencyMs)
	metrics.SetRelayScore(relayID, score.Score)
	metrics.IncrementPublishSuccess()
	s.selector.RecordPublishAttempt(true)
	metrics.SetPublishWidth(s.selector.CurrentWidth())

	// Persist
	s.persistScore(score)
}

// RecordPublishFailure records a failed publish to a relay.
func (s *ScoringService) RecordPublishFailure(relayID string) {
	score := s.GetOrCreateScore(relayID)
	score.RecordFailure()
	metrics.SetRelayScore(relayID, score.Score)
	metrics.IncrementPublishFailure()
	s.selector.RecordPublishAttempt(false)
	metrics.SetPublishWidth(s.selector.CurrentWidth())

	// Persist
	s.persistScore(score)
}

// RecordDisconnect records a disconnect from a relay.
func (s *ScoringService) RecordDisconnect(relayID string) {
	score := s.GetOrCreateScore(relayID)
	score.RecordDisconnect()
	metrics.SetRelayScore(relayID, score.Score)

	// Persist
	s.persistScore(score)
}

// RecordHandshakeFailure records a handshake failure with a relay.
func (s *ScoringService) RecordHandshakeFailure(relayID string) {
	score := s.GetOrCreateScore(relayID)
	score.RecordHandshakeFailure()
	metrics.SetRelayScore(relayID, score.Score)

	// Persist
	s.persistScore(score)
}

// RecordMessageAccepted records a message accepted by a relay (for blackhole detection).
func (s *ScoringService) RecordMessageAccepted(relayID string) {
	score := s.GetOrCreateScore(relayID)
	score.RecordMessageAccepted()

	// Check for blackhole behavior
	if score.IsBlackhole() {
		log.Printf("scoring: blackhole detected for relay %s", relayID)
		metrics.SetRelayScore(relayID, score.Score)
	}

	// Persist
	s.persistScore(score)
}

// RecordMessageForwarded records a message forwarded by a relay.
func (s *ScoringService) RecordMessageForwarded(relayID string) {
	score := s.GetOrCreateScore(relayID)
	score.RecordMessageForwarded()

	// Persist
	s.persistScore(score)
}

// persistScore persists a score to the store.
func (s *ScoringService) persistScore(score *model.RelayScore) {
	if s.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.store.PutScore(ctx, score); err != nil {
		log.Printf("scoring: failed to persist score: %v", err)
	}
}

// GetTopRelays returns the top N relays by score.
func (s *ScoringService) GetTopRelays(n int) []*model.RelayScore {
	s.scoresMu.RLock()
	defer s.scoresMu.RUnlock()

	scores := make([]*model.RelayScore, 0, len(s.scores))
	for _, score := range s.scores {
		scores = append(scores, score)
	}

	// Sort by score descending
	model.SortByScore(scores)

	if len(scores) < n {
		n = len(scores)
	}

	return scores[:n]
}

// GetRelaysForPublish returns relays selected for publishing based on current width.
func (s *ScoringService) GetRelaysForPublish() []*model.RelayScore {
	width := s.selector.CurrentWidth()
	allScores := s.GetTopRelays(width * 2) // Get more than needed for selection

	return model.SelectRelays(allScores, width)
}

// GetCurrentWidth returns the current adaptive publish width.
func (s *ScoringService) GetCurrentWidth() int {
	return s.selector.CurrentWidth()
}

// RemoveRelay removes a relay's score.
func (s *ScoringService) RemoveRelay(relayID string) {
	s.scoresMu.Lock()
	defer s.scoresMu.Unlock()

	delete(s.scores, relayID)
	metrics.RemoveRelayScore(relayID)

	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.store.RemoveScore(ctx, relayID)
	}
}

// GetScore returns the score for a specific relay.
func (s *ScoringService) GetScore(relayID string) *model.RelayScore {
	s.scoresMu.RLock()
	defer s.scoresMu.RUnlock()

	return s.scores[relayID]
}
