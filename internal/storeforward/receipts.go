package storeforward

import (
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// RelayAckOutcome stores relay-to-relay receipt telemetry for an envelope.
type RelayAckOutcome struct {
	PeerID      string
	EnvelopeID  string
	Destination string
	Status      string
	At          time.Time
}

// RecordRelayAck stores a `relay_ack` outcome for federation forwarding telemetry.
// This is relay-to-relay envelope handling feedback and is separate from client `ack`
// delivery state.
func (e *Engine) RecordRelayAck(peerID string, frame *model.RelayAckFrame) {
	if frame == nil || frame.EnvelopeID == "" {
		return
	}

	outcome := RelayAckOutcome{
		PeerID:      peerID,
		EnvelopeID:  frame.EnvelopeID,
		Destination: frame.Destination,
		Status:      frame.Status,
		At:          e.nowUTC(),
	}

	e.relayAckMu.Lock()
	e.relayAcks[relayAckKey(frame.EnvelopeID, frame.Destination, peerID)] = outcome
	e.relayAckMu.Unlock()
}

// RelayAckFor returns the latest recorded relay-to-relay ack telemetry.
func (e *Engine) RelayAckFor(envelopeID string, destination string, peerID string) (RelayAckOutcome, bool) {
	e.relayAckMu.RLock()
	outcome, ok := e.relayAcks[relayAckKey(envelopeID, destination, peerID)]
	e.relayAckMu.RUnlock()
	return outcome, ok
}

func relayAckKey(envelopeID, destination, peerID string) string {
	return envelopeID + deliveryIdentityDelimiter + destination + deliveryIdentityDelimiter + peerID
}
