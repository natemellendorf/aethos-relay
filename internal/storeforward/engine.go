package storeforward

import (
	"sync"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	defaultPullLimit = 50
	maxPullLimit     = 100
)

// Engine owns store-and-forward decisions shared by client and federation paths.
type Engine struct {
	store         store.Store
	envelopeStore store.EnvelopeStore
	relayID       string
	maxTTL        time.Duration

	now func() time.Time

	relayAckMu sync.RWMutex
	relayAcks  map[string]RelayAckOutcome
}

// New creates a store-and-forward engine.
func New(messageStore store.Store, maxTTL time.Duration) *Engine {
	return &Engine{
		store:     messageStore,
		maxTTL:    maxTTL,
		now:       time.Now,
		relayAcks: make(map[string]RelayAckOutcome),
	}
}

// ConfigureFederation enables federation envelope and relay receipt handling.
func (e *Engine) ConfigureFederation(relayID string, envelopeStore store.EnvelopeStore) {
	e.relayID = relayID
	e.envelopeStore = envelopeStore
}

func (e *Engine) nowUTC() time.Time {
	return e.now().UTC()
}

func (e *Engine) setNowForTests(now func() time.Time) {
	if now == nil {
		e.now = time.Now
		return
	}
	e.now = now
}
