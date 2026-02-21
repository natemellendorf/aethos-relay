package store

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// EncodeMessage encodes a Message to bytes.
func EncodeMessage(msg *model.Message) ([]byte, error) {
	buf := new(bytes.Buffer)

	// ID (string length + string)
	idLen := uint16(len(msg.ID))
	binary.Write(buf, binary.BigEndian, idLen)
	buf.WriteString(msg.ID)

	// From
	fromLen := uint16(len(msg.From))
	binary.Write(buf, binary.BigEndian, fromLen)
	buf.WriteString(msg.From)

	// To
	toLen := uint16(len(msg.To))
	binary.Write(buf, binary.BigEndian, toLen)
	buf.WriteString(msg.To)

	// Payload
	payloadLen := uint16(len(msg.Payload))
	binary.Write(buf, binary.BigEndian, payloadLen)
	buf.WriteString(msg.Payload)

	// CreatedAt (Unix timestamp)
	createdAtUnix := msg.CreatedAt.Unix()
	binary.Write(buf, binary.BigEndian, createdAtUnix)

	// ExpiresAt (Unix timestamp)
	expiresAtUnix := msg.ExpiresAt.Unix()
	binary.Write(buf, binary.BigEndian, expiresAtUnix)

	// Delivered (bool as byte)
	if msg.Delivered {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}

	return buf.Bytes(), nil
}

// DecodeMessage decodes bytes to a Message.
func DecodeMessage(data []byte) (*model.Message, error) {
	buf := bytes.NewReader(data)

	msg := &model.Message{}

	// ID
	var idLen uint16
	if err := binary.Read(buf, binary.BigEndian, &idLen); err != nil {
		return nil, err
	}
	idBytes := make([]byte, idLen)
	if _, err := buf.Read(idBytes); err != nil {
		return nil, err
	}
	msg.ID = string(idBytes)

	// From
	var fromLen uint16
	if err := binary.Read(buf, binary.BigEndian, &fromLen); err != nil {
		return nil, err
	}
	fromBytes := make([]byte, fromLen)
	if _, err := buf.Read(fromBytes); err != nil {
		return nil, err
	}
	msg.From = string(fromBytes)

	// To
	var toLen uint16
	if err := binary.Read(buf, binary.BigEndian, &toLen); err != nil {
		return nil, err
	}
	toBytes := make([]byte, toLen)
	if _, err := buf.Read(toBytes); err != nil {
		return nil, err
	}
	msg.To = string(toBytes)

	// Payload
	var payloadLen uint16
	if err := binary.Read(buf, binary.BigEndian, &payloadLen); err != nil {
		return nil, err
	}
	payloadBytes := make([]byte, payloadLen)
	if _, err := buf.Read(payloadBytes); err != nil {
		return nil, err
	}
	msg.Payload = string(payloadBytes)

	// CreatedAt
	var createdAtUnix int64
	if err := binary.Read(buf, binary.BigEndian, &createdAtUnix); err != nil {
		return nil, err
	}
	msg.CreatedAt = time.Unix(createdAtUnix, 0)

	// ExpiresAt
	var expiresAtUnix int64
	if err := binary.Read(buf, binary.BigEndian, &expiresAtUnix); err != nil {
		return nil, err
	}
	msg.ExpiresAt = time.Unix(expiresAtUnix, 0)

	// Delivered
	deliveredByte, err := buf.ReadByte()
	if err != nil {
		return nil, err
	}
	msg.Delivered = deliveredByte == 1

	return msg, nil
}

// EncodeQueueKey encodes a QueueKey to bytes.
func EncodeQueueKey(key *model.QueueKey) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(key.To)
	buf.WriteByte(0) // separator
	buf.WriteString(key.CreatedAt.Format(time.RFC3339Nano))
	buf.WriteByte(0) // separator
	buf.WriteString(key.MsgID)
	return buf.Bytes()
}

// DecodeQueueKey decodes bytes to a QueueKey.
func DecodeQueueKey(data []byte) (*model.QueueKey, error) {
	parts := bytes.Split(data, []byte{0})
	if len(parts) != 3 {
		return nil, ErrInvalidKey
	}
	createdAt, err := time.Parse(time.RFC3339Nano, string(parts[1]))
	if err != nil {
		return nil, err
	}
	return &model.QueueKey{
		To:        string(parts[0]),
		CreatedAt: createdAt,
		MsgID:     string(parts[2]),
	}, nil
}

// EncodeExpiryKey encodes an expiry key (expires_at|msg_id).
func EncodeExpiryKey(msgID string, expiresAt time.Time) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(expiresAt.Format(time.RFC3339Nano))
	buf.WriteByte(0)
	buf.WriteString(msgID)
	return buf.Bytes()
}

// EncodeMsgIDKey encodes a msg_id lookup key.
func EncodeMsgIDKey(msgID string) []byte {
	return []byte(msgID)
}

// EncodeDeliveryKey encodes a per-device delivery key (msgID|recipientID).
func EncodeDeliveryKey(msgID string, recipientID string) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(msgID)
	buf.WriteByte(0) // separator
	buf.WriteString(recipientID)
	return buf.Bytes()
}

// DecodeDeliveryKey decodes a per-device delivery key.
func DecodeDeliveryKey(data []byte) (msgID string, recipientID string, err error) {
	parts := bytes.Split(data, []byte{0})
	if len(parts) != 2 {
		return "", "", ErrInvalidKey
	}
	return string(parts[0]), string(parts[1]), nil
}

// EncodeDescriptor encodes a RelayDescriptor to bytes using JSON.
func EncodeDescriptor(d *model.RelayDescriptor) ([]byte, error) {
	return []byte(d.RelayID + "\x00" + d.WSURL + "\x00" + d.Region + "\x00" +
		joinStrings(d.Tags) + "\x00" +
		d.FirstSeenAt.Format(time.RFC3339Nano) + "\x00" +
		d.LastSeenAt.Format(time.RFC3339Nano) + "\x00" +
		d.ExpiresAt.Format(time.RFC3339Nano) + "\x00" +
		d.AdvertisedBy), nil
}

// DecodeDescriptor decodes bytes to a RelayDescriptor.
func DecodeDescriptor(data []byte) (*model.RelayDescriptor, error) {
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 7 {
		return nil, ErrInvalidKey
	}

	firstSeen, err := time.Parse(time.RFC3339Nano, string(parts[4]))
	if err != nil {
		return nil, err
	}
	lastSeen, err := time.Parse(time.RFC3339Nano, string(parts[5]))
	if err != nil {
		return nil, err
	}
	expires, err := time.Parse(time.RFC3339Nano, string(parts[6]))
	if err != nil {
		return nil, err
	}

	return &model.RelayDescriptor{
		RelayID:      string(parts[0]),
		WSURL:        string(parts[1]),
		Region:       string(parts[2]),
		Tags:         splitStrings(string(parts[3])),
		FirstSeenAt:  firstSeen,
		LastSeenAt:   lastSeen,
		ExpiresAt:    expires,
		AdvertisedBy: string(parts[7]),
	}, nil
}

// EncodeDescriptorExpiryKey encodes a descriptor expiry key.
func EncodeDescriptorExpiryKey(relayID string, expiresAt time.Time) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(expiresAt.Format(time.RFC3339Nano))
	buf.WriteByte(0)
	buf.WriteString(relayID)
	return buf.Bytes()
}

// EncodeDescriptorPeerKey encodes a descriptor peer index key.
func EncodeDescriptorPeerKey(peerID string, timestamp time.Time) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(peerID)
	buf.WriteByte(0)
	buf.WriteString(timestamp.Format(time.RFC3339Nano))
	return buf.Bytes()
}

// joinStrings joins a string slice with a delimiter.
func joinStrings(s []string) string {
	if len(s) == 0 {
		return ""
	}
	result := s[0]
	for i := 1; i < len(s); i++ {
		result += "," + s[i]
	}
	return result
}

// splitStrings splits a comma-separated string into a slice.
func splitStrings(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range splitStr(s, ",") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// splitStr splits a string by delimiter (simple implementation).
func splitStr(s, sep string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	result = append(result, s[start:])
	return result
}
