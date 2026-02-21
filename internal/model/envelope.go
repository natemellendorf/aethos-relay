package model

import (
	"bytes"
	"encoding/binary"
	"time"
)

// EnvelopeBucket keys for bbolt storage
var (
	BucketEnvelopes     = []byte("envelopes")
	BucketByDestination = []byte("by_destination")
	BucketByExpiry      = []byte("by_expiry")
	BucketSeen          = []byte("seen")
	BucketMeta          = []byte("meta")
	MetaLastSweep       = []byte("last_sweep")
)

// Envelope represents a federation message envelope that is persisted.
// The relay never decrypts or inspects the payload - it's opaque bytes.
type Envelope struct {
	ID              string    `json:"id"`              // Unique envelope ID
	DestinationID   string    `json:"destination_id"`  // Recipient wayfarer ID (or hash)
	OpaquePayload   []byte    `json:"payload"`         // Opaque bytes (relay never decrypts)
	OriginRelayID   string    `json:"origin_relay_id"` // Original relay that received the message
	CurrentHopCount int       `json:"hop_count"`       // Current hop count
	CreatedAt       time.Time `json:"created_at"`      // When envelope was first created
	ExpiresAt       time.Time `json:"expires_at"`      // TTL expiration
}

// Validate validates an envelope.
// Returns error if envelope is invalid.
func (e *Envelope) Validate() error {
	// Early exit: check required fields
	if e.ID == "" {
		return ErrEnvelopeInvalidID
	}
	if e.DestinationID == "" {
		return ErrEnvelopeInvalidDestination
	}
	if e.CreatedAt.IsZero() {
		return ErrEnvelopeInvalidTimestamp
	}
	if e.ExpiresAt.IsZero() {
		return ErrEnvelopeInvalidTimestamp
	}
	if e.ExpiresAt.Before(e.CreatedAt) {
		return ErrEnvelopeInvalidTimestamp
	}
	return nil
}

// IsExpired returns true if the envelope has expired.
func (e *Envelope) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// Constants for envelope limits
const (
	// MAX_HOPS is the maximum number of hops allowed for federation forwarding.
	MAX_HOPS = 5

	// MAX_ENVELOPE_SIZE is the maximum size of an envelope payload in bytes.
	MAX_ENVELOPE_SIZE = 64 * 1024 // 64KB

	// MAX_TTL_SECONDS is the maximum TTL for envelopes.
	MAX_TTL_SECONDS = 604800 // 7 days

	// MAX_FEDERATION_PEERS is the maximum number of federation peers.
	MAX_FEDERATION_PEERS = 50

	// DEFAULT_TTL_SECONDS is the default TTL for envelopes.
	DEFAULT_TTL_SECONDS = 86400 // 1 day
)

// Envelope validation errors
var (
	ErrEnvelopeInvalidID          = &EnvelopeError{"envelope id is required"}
	ErrEnvelopeInvalidDestination = &EnvelopeError{"destination_id is required"}
	ErrEnvelopeInvalidTimestamp   = &EnvelopeError{"invalid timestamp"}
	ErrEnvelopeHopLimitExceeded   = &EnvelopeError{"hop limit exceeded"}
	ErrEnvelopeTooLarge           = &EnvelopeError{"envelope payload too large"}
	ErrEnvelopeExpired            = &EnvelopeError{"envelope has expired"}
)

// EnvelopeError represents an envelope validation error.
type EnvelopeError struct {
	Message string
}

func (e *EnvelopeError) Error() string {
	return e.Message
}

// EnvelopeCodec provides encoding/decoding for Envelope in bbolt.
type EnvelopeCodec struct{}

// EncodeEnvelope encodes an envelope to bytes.
func (c *EnvelopeCodec) EncodeEnvelope(env *Envelope) ([]byte, error) {
	var buf bytes.Buffer

	// Write ID length + ID
	idLen := uint16(len(env.ID))
	binary.Write(&buf, binary.BigEndian, idLen)
	buf.WriteString(env.ID)

	// Write DestinationID length + DestinationID
	destLen := uint16(len(env.DestinationID))
	binary.Write(&buf, binary.BigEndian, destLen)
	buf.WriteString(env.DestinationID)

	// Write OriginRelayID length + OriginRelayID
	originLen := uint16(len(env.OriginRelayID))
	binary.Write(&buf, binary.BigEndian, originLen)
	buf.WriteString(env.OriginRelayID)

	// Write payload length + payload
	payloadLen := uint32(len(env.OpaquePayload))
	binary.Write(&buf, binary.BigEndian, payloadLen)
	buf.Write(env.OpaquePayload)

	// Write hop count
	binary.Write(&buf, binary.BigEndian, uint16(env.CurrentHopCount))

	// Write timestamps (Unixnano)
	binary.Write(&buf, binary.BigEndian, env.CreatedAt.UnixNano())
	binary.Write(&buf, binary.BigEndian, env.ExpiresAt.UnixNano())

	return buf.Bytes(), nil
}

// DecodeEnvelope decodes bytes to an envelope.
func (c *EnvelopeCodec) DecodeEnvelope(data []byte) (*Envelope, error) {
	if len(data) < 2 {
		return nil, ErrEnvelopeInvalidID
	}

	env := &Envelope{}
	buf := bytes.NewBuffer(data)

	// Read ID
	var idLen uint16
	binary.Read(buf, binary.BigEndian, &idLen)
	if len(buf.Bytes()) < int(idLen) {
		return nil, ErrEnvelopeInvalidID
	}
	env.ID = string(buf.Next(int(idLen)))

	// Read DestinationID
	var destLen uint16
	binary.Read(buf, binary.BigEndian, &destLen)
	if len(buf.Bytes()) < int(destLen) {
		return nil, ErrEnvelopeInvalidDestination
	}
	env.DestinationID = string(buf.Next(int(destLen)))

	// Read OriginRelayID
	var originLen uint16
	binary.Read(buf, binary.BigEndian, &originLen)
	if len(buf.Bytes()) < int(originLen) {
		return nil, ErrEnvelopeInvalidDestination
	}
	env.OriginRelayID = string(buf.Next(int(originLen)))

	// Read payload
	var payloadLen uint32
	binary.Read(buf, binary.BigEndian, &payloadLen)
	if len(buf.Bytes()) < int(payloadLen) {
		return nil, ErrEnvelopeTooLarge
	}
	env.OpaquePayload = buf.Next(int(payloadLen))

	// Read hop count
	var hopCount uint16
	binary.Read(buf, binary.BigEndian, &hopCount)
	env.CurrentHopCount = int(hopCount)

	// Read timestamps
	var createdUnix, expiresUnix int64
	binary.Read(buf, binary.BigEndian, &createdUnix)
	binary.Read(buf, binary.BigEndian, &expiresUnix)
	env.CreatedAt = time.Unix(0, createdUnix)
	env.ExpiresAt = time.Unix(0, expiresUnix)

	return env, nil
}

// DestinationIndexKey creates a composite key for the by_destination bucket.
func DestinationIndexKey(destinationID string, msgID string) []byte {
	var buf bytes.Buffer
	buf.WriteString(destinationID)
	buf.WriteByte(0)
	buf.WriteString(msgID)
	return buf.Bytes()
}

// ExpiryIndexKey creates a composite key for the by_expiry bucket.
func ExpiryIndexKey(expiresAt time.Time, msgID string) []byte {
	var buf bytes.Buffer
	buf.WriteString(expiresAt.Format(time.RFC3339Nano))
	buf.WriteByte(0)
	buf.WriteString(msgID)
	return buf.Bytes()
}

// SeenKey creates a key for the seen bucket (envelope ID + relay ID).
func SeenKey(envelopeID string, relayID string) []byte {
	var buf bytes.Buffer
	buf.WriteString(envelopeID)
	buf.WriteByte(0)
	buf.WriteString(relayID)
	return buf.Bytes()
}
