package storeforward

import (
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	defaultPullLimit = 50
	maxPullLimit     = 100
)

// Engine owns store-and-forward decisions shared by client and federation paths.
type Engine struct {
	store                store.Store
	envelopeStore        store.EnvelopeStore
	relayID              string
	maxTTL               time.Duration
	ackDrivenSuppression bool

	now func() time.Time
}

// New creates a store-and-forward engine.
func New(messageStore store.Store, maxTTL time.Duration) *Engine {
	return &Engine{
		store:  messageStore,
		maxTTL: maxTTL,
		now:    time.Now,
	}
}

// SetAckDrivenSuppression switches queue suppression from legacy push-delivered
// state to canonical durable ack state.
func (e *Engine) SetAckDrivenSuppression(enabled bool) {
	e.ackDrivenSuppression = enabled
}

// IsAckDrivenSuppression reports whether canonical ack-driven suppression is enabled.
func (e *Engine) IsAckDrivenSuppression() bool {
	return e.ackDrivenSuppression
}

// ConfigureFederation enables federation envelope and relay receipt handling.
func (e *Engine) ConfigureFederation(relayID string, envelopeStore store.EnvelopeStore) {
	e.relayID = relayID
	e.envelopeStore = envelopeStore
}

func (e *Engine) setNowForTests(now func() time.Time) {
	if now == nil {
		e.now = time.Now
		return
	}
	e.now = now
}
