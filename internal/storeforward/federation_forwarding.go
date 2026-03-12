package storeforward

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

var (
	ErrForwardMessageInvalid  = errors.New("relay forward message invalid")
	ErrForwardMessageTooLarge = errors.New("relay forward payload too large")
)

type relayIngestAtomicMarker interface {
	MarkSeenAndRelayIngestEmitted(ctx context.Context, itemID string, relayIDs []string) (bool, error)
}

// RelayForwardStatus captures how an inbound forwarded payload was handled.
type RelayForwardStatus string

const (
	RelayForwardAccepted  RelayForwardStatus = "accepted"
	RelayForwardDuplicate RelayForwardStatus = "duplicate"
	RelayForwardExpired   RelayForwardStatus = "expired"
	RelayForwardSeenLoop  RelayForwardStatus = "seen_loop"
	RelayForwardInvalid   RelayForwardStatus = "invalid"
	RelayForwardTooLarge  RelayForwardStatus = "too_large"
)

// RelayForwardResult reports the result of handling a forwarded payload.
type RelayForwardResult struct {
	Status   RelayForwardStatus
	Envelope *model.Envelope
}

// AcceptRelayForward validates and persists an inbound relay forward payload.
func (e *Engine) AcceptRelayForward(ctx context.Context, sourceRelayID string, msg *model.Message, maxPayloadSize int) (RelayForwardResult, error) {
	if !isValidForwardMessage(msg) {
		return RelayForwardResult{Status: RelayForwardInvalid}, ErrForwardMessageInvalid
	}
	if maxPayloadSize > 0 && len(msg.Payload) > maxPayloadSize {
		return RelayForwardResult{Status: RelayForwardTooLarge}, ErrForwardMessageTooLarge
	}

	now := e.now()
	if now.After(msg.ExpiresAt) {
		return RelayForwardResult{Status: RelayForwardExpired}, nil
	}

	seenBySource := false
	if e.envelopeStore != nil && sourceRelayID != "" {
		seen, err := e.envelopeStore.IsSeenBy(ctx, msg.ID, sourceRelayID)
		if err != nil {
			return RelayForwardResult{}, err
		}
		seenBySource = seen
	}

	messageAlreadyExists := false
	existing, err := e.store.GetMessageByID(ctx, msg.ID)
	switch {
	case err == nil && existing != nil:
		messageAlreadyExists = true
	case err != nil && !isNotFoundErr(err):
		return RelayForwardResult{}, err
	}

	if !messageAlreadyExists {
		if seenBySource {
			return RelayForwardResult{Status: RelayForwardSeenLoop}, nil
		}
		if err := e.store.PersistMessage(ctx, msg); err != nil {
			return RelayForwardResult{}, err
		}
	}

	envelope, err := e.persistEnvelopeState(ctx, msg, sourceRelayID)
	if err != nil {
		return RelayForwardResult{}, err
	}

	markerCreated := false
	if e.envelopeStore != nil {
		relayIDs := relayIngestSeenRelayIDs(sourceRelayID, e.relayID)
		if atomicMarker, ok := e.envelopeStore.(relayIngestAtomicMarker); ok {
			markerCreated, err = atomicMarker.MarkSeenAndRelayIngestEmitted(ctx, relayIngestItemID(msg), relayIDs)
			if err != nil {
				return RelayForwardResult{}, err
			}
		} else {
			for _, relayID := range relayIDs {
				if err := e.envelopeStore.MarkSeen(ctx, msg.ID, relayID); err != nil {
					return RelayForwardResult{}, err
				}
			}

			markerCreated, err = e.envelopeStore.MarkRelayIngestEmitted(ctx, relayIngestItemID(msg))
			if err != nil {
				return RelayForwardResult{}, err
			}
		}
	} else if !messageAlreadyExists {
		markerCreated = true
	}

	if markerCreated {
		e.emitRelayIngest(ctx, RelayIngestSignal{
			ItemID:      relayIngestItemID(msg),
			Trusted:     isAuthenticatedRelayContext(sourceRelayID),
			SourceRelay: sourceRelayID,
		})
	}

	status := RelayForwardAccepted
	if messageAlreadyExists {
		if seenBySource {
			status = RelayForwardSeenLoop
		} else {
			status = RelayForwardDuplicate
		}
	}

	return RelayForwardResult{Status: status, Envelope: envelope}, nil
}

// PrepareForwardingEnvelope increments local hop state for an outbound forward.
func (e *Engine) PrepareForwardingEnvelope(ctx context.Context, msg *model.Message, maxHops int) (*model.Envelope, error) {
	if !isValidForwardMessage(msg) {
		return nil, ErrForwardMessageInvalid
	}

	envelope, err := e.persistEnvelopeState(ctx, msg, e.relayID)
	if err != nil {
		return nil, err
	}

	if e.now().After(envelope.ExpiresAt) {
		return nil, model.ErrEnvelopeExpired
	}

	nextHop := envelope.CurrentHopCount + 1
	if maxHops > 0 && nextHop > maxHops {
		return nil, model.ErrEnvelopeHopLimitExceeded
	}
	envelope.CurrentHopCount = nextHop

	if e.envelopeStore != nil {
		if err := e.envelopeStore.PersistEnvelope(ctx, envelope); err != nil {
			return nil, err
		}
		if e.relayID != "" {
			if err := e.envelopeStore.MarkSeen(ctx, envelope.ID, e.relayID); err != nil {
				return nil, err
			}
		}
	}

	return envelope, nil
}

// ReserveForwardingCandidate marks relay target as seen and returns false if already seen.
func (e *Engine) ReserveForwardingCandidate(ctx context.Context, envelopeID string, relayID string) (bool, error) {
	if e.envelopeStore == nil || envelopeID == "" || relayID == "" {
		return true, nil
	}

	seen, err := e.envelopeStore.IsSeenBy(ctx, envelopeID, relayID)
	if err != nil {
		return false, err
	}
	if seen {
		return false, nil
	}

	if err := e.envelopeStore.MarkSeen(ctx, envelopeID, relayID); err != nil {
		return false, err
	}

	return true, nil
}

// SweepExpiredEnvelopes removes expired envelopes and returns remove count.
func (e *Engine) SweepExpiredEnvelopes(ctx context.Context, before time.Time) (int, error) {
	if e.envelopeStore == nil {
		return 0, nil
	}

	expired, err := e.envelopeStore.GetExpiredEnvelopes(ctx, before)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, env := range expired {
		if err := e.envelopeStore.RemoveEnvelope(ctx, env.ID); err != nil {
			return removed, err
		}
		removed++
	}

	return removed, nil
}

func (e *Engine) persistEnvelopeState(ctx context.Context, msg *model.Message, originRelayID string) (*model.Envelope, error) {
	envelope := &model.Envelope{}
	if e.envelopeStore != nil {
		stored, err := e.envelopeStore.GetEnvelopeByID(ctx, msg.ID)
		switch {
		case err == nil && stored != nil:
			envelope = stored
		case err != nil && !isNotFoundErr(err):
			return nil, err
		default:
			envelope = envelopeFromMessage(msg, originRelayID)
		}

		if envelope.OriginRelayID == "" {
			envelope.OriginRelayID = originRelayID
		}

		if err := e.envelopeStore.PersistEnvelope(ctx, envelope); err != nil {
			return nil, err
		}
		return envelope, nil
	}

	envelope = envelopeFromMessage(msg, originRelayID)
	return envelope, nil
}

func envelopeFromMessage(msg *model.Message, originRelayID string) *model.Envelope {
	return &model.Envelope{
		ID:              msg.ID,
		DestinationID:   msg.To,
		OpaquePayload:   []byte(msg.Payload),
		OriginRelayID:   originRelayID,
		CurrentHopCount: 0,
		CreatedAt:       msg.CreatedAt,
		ExpiresAt:       msg.ExpiresAt,
	}
}

func isValidForwardMessage(msg *model.Message) bool {
	if msg == nil {
		return false
	}
	if msg.ID == "" || msg.From == "" || msg.To == "" || msg.Payload == "" {
		return false
	}
	if msg.CreatedAt.IsZero() || msg.ExpiresAt.IsZero() {
		return false
	}
	return true
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	// TODO: switch to errors.Is when store exports typed not-found sentinels.
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func relayIngestItemID(msg *model.Message) string {
	if msg == nil {
		return ""
	}

	// Phase 2 defines RELAY_INGEST item_id as the durable message key.
	return msg.ID
}

func isAuthenticatedRelayContext(sourceRelayID string) bool {
	// Phase 2 trust boundary: only authenticated relay transport contexts are
	// trusted RELAY_INGEST producers. Today, a non-empty source relay ID is
	// supplied only by validated federation sessions.
	return sourceRelayID != ""
}

func relayIngestSeenRelayIDs(sourceRelayID string, localRelayID string) []string {
	seen := make(map[string]struct{}, 2)
	if sourceRelayID != "" {
		seen[sourceRelayID] = struct{}{}
	}
	if localRelayID != "" {
		seen[localRelayID] = struct{}{}
	}

	relayIDs := make([]string, 0, len(seen))
	for relayID := range seen {
		relayIDs = append(relayIDs, relayID)
	}
	sort.Strings(relayIDs)
	return relayIDs
}
