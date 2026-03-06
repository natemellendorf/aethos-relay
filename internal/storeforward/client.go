package storeforward

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

const deliveryIdentityDelimiter = "\x00"

// DeliveryIdentity builds a device-specific identity for delivery tracking.
func DeliveryIdentity(wayfarerID, deviceID string) string {
	if deviceID == "" {
		return wayfarerID
	}
	return wayfarerID + deliveryIdentityDelimiter + deviceID
}

// QueueRecipient extracts queue recipient identity from a delivery identity.
func QueueRecipient(deliveryIdentity string) string {
	idx := strings.Index(deliveryIdentity, deliveryIdentityDelimiter)
	if idx < 0 {
		return deliveryIdentity
	}
	return deliveryIdentity[:idx]
}

// NormalizePullLimit clamps the pull limit to relay defaults.
func NormalizePullLimit(limit int) int {
	if limit <= 0 || limit > maxPullLimit {
		return defaultPullLimit
	}
	return limit
}

// AcceptClientSend creates a message from a client `send` frame.
func (e *Engine) AcceptClientSend(from, to, payloadB64 string, ttlSeconds int) (*model.Message, time.Duration) {
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 || ttl > e.maxTTL {
		ttl = e.maxTTL
	}

	now := e.nowUTC()
	msg := &model.Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        to,
		Payload:   payloadB64,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Delivered: false,
	}

	return msg, ttl
}

// PersistMessage stores a queued message.
func (e *Engine) PersistMessage(ctx context.Context, msg *model.Message) error {
	return e.store.PersistMessage(ctx, msg)
}

// PullForDeliveryIdentity returns queued, non-expired messages for a delivery identity.
func (e *Engine) PullForDeliveryIdentity(ctx context.Context, deliveryIdentity string, limit int) ([]*model.Message, error) {
	queueRecipient := QueueRecipient(deliveryIdentity)
	messages, err := e.store.GetQueuedMessages(ctx, queueRecipient, NormalizePullLimit(limit))
	if err != nil {
		return nil, err
	}

	now := e.nowUTC()
	if deliveryIdentity == queueRecipient {
		var filtered []*model.Message
		for _, msg := range messages {
			if now.After(msg.ExpiresAt) {
				continue
			}
			filtered = append(filtered, msg)
		}
		return filtered, nil
	}

	var filtered []*model.Message
	for _, msg := range messages {
		if now.After(msg.ExpiresAt) {
			continue
		}
		delivered, err := e.store.IsDeliveredTo(ctx, msg.ID, deliveryIdentity)
		if err != nil {
			return nil, err
		}
		if delivered {
			continue
		}
		filtered = append(filtered, msg)
	}

	return filtered, nil
}

// AckClientDelivery records a client `ack` for a single delivery identity.
func (e *Engine) AckClientDelivery(ctx context.Context, msgID string, deliveryIdentity string) error {
	return e.store.MarkDelivered(ctx, msgID, deliveryIdentity)
}

// MarkDelivery records delivery for a single delivery identity.
func (e *Engine) MarkDelivery(ctx context.Context, msgID string, deliveryIdentity string) error {
	return e.store.MarkDelivered(ctx, msgID, deliveryIdentity)
}
