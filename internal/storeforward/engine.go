package storeforward

import (
	"context"
	"sync"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	defaultPullLimit = 50
	maxPullLimit     = 100
)

// RelayIngestSignal is the relay-side ingest effect emitted after durable ingest.
//
// Phase 2 intentionally models RELAY_INGEST as an internal signal instead of
// a relay-to-relay protocol frame emission, because propagation policy remains
// disabled from Phase 1.
type RelayIngestSignal struct {
	ItemID      string
	Trusted     bool
	SourceRelay string
}

// RelayIngestObserver receives durable relay ingest signals.
type RelayIngestObserver func(context.Context, RelayIngestSignal)

// Engine owns store-and-forward decisions shared by client and federation paths.
type Engine struct {
	store                 store.Store
	envelopeStore         store.EnvelopeStore
	relayID               string
	maxTTL                time.Duration
	ackDrivenSuppression  bool
	relayIngestObserverMu sync.RWMutex
	relayIngestObserver   RelayIngestObserver

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

// SetRelayIngestObserver configures an observer for durable relay ingest effects.
func (e *Engine) SetRelayIngestObserver(observer RelayIngestObserver) {
	e.relayIngestObserverMu.Lock()
	defer e.relayIngestObserverMu.Unlock()
	e.relayIngestObserver = observer
}

func (e *Engine) emitRelayIngest(ctx context.Context, signal RelayIngestSignal) {
	e.relayIngestObserverMu.RLock()
	observer := e.relayIngestObserver
	e.relayIngestObserverMu.RUnlock()
	if observer == nil {
		return
	}
	observer(ctx, signal)
}

func (e *Engine) setNowForTests(now func() time.Time) {
	if now == nil {
		e.now = time.Now
		return
	}
	e.now = now
}
