// Package clientv1 is a temporary compatibility boundary for client WebSocket
// protocol shape decisions. Canonical protocol semantics live outside this
// package.
package clientv1

import (
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

// DeliveryIdentity preserves the client v1 delivery identity compatibility rule.
func DeliveryIdentity(wayfarerID, deviceID string) string {
	return storeforward.DeliveryIdentity(wayfarerID, deviceID)
}

// EncodeSendOK encodes send_ok with legacy and canonical timestamp aliases.
func EncodeSendOK(msg *model.Message) model.WSFrame {
	if msg == nil {
		return model.WSFrame{Type: model.FrameTypeSendOK}
	}

	receivedAt := receivedAtUnix(msg)
	return model.WSFrame{
		Type:       model.FrameTypeSendOK,
		MsgID:      msg.ID,
		At:         receivedAt,
		ReceivedAt: receivedAt,
		ExpiresAt:  msg.ExpiresAt.Unix(),
	}
}

// EncodePushMessage encodes a client-facing message frame with dual timestamps.
func EncodePushMessage(msg *model.Message) model.WSFrame {
	if msg == nil {
		return model.WSFrame{Type: model.FrameTypeMessage}
	}

	receivedAt := receivedAtUnix(msg)
	return model.WSFrame{
		Type:       model.FrameTypeMessage,
		MsgID:      msg.ID,
		From:       msg.From,
		PayloadB64: msg.Payload,
		At:         receivedAt,
		ReceivedAt: receivedAt,
	}
}

// EncodePullEntry encodes a pull entry that keeps legacy Message fields and
// adds canonical received_at.
func EncodePullEntry(msg *model.Message) model.WSPullMessage {
	if msg == nil {
		return model.WSPullMessage{}
	}

	return model.WSPullMessage{
		Message:    *msg,
		ReceivedAt: receivedAtUnix(msg),
	}
}

// EncodeError encodes the legacy error schema shape.
func EncodeError(errText string) model.WSFrame {
	return model.WSFrame{
		Type:  model.FrameTypeError,
		MsgID: errText,
	}
}

// ResolveAckRecipient resolves ack recipient identity using tracked delivery
// identity first, with legacy wayfarer fallback.
func ResolveAckRecipient(c *model.Client, msgID string) string {
	if c == nil {
		return ""
	}

	if recipientID := c.MessageDeliveryRecipient(msgID); recipientID != "" {
		return recipientID
	}

	return c.WayfarerID
}

func receivedAtUnix(msg *model.Message) int64 {
	if msg == nil {
		return 0
	}
	return msg.CreatedAt.Unix()
}
