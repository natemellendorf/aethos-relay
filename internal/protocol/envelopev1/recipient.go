package envelopev1

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	canonicalBytesVersion = 0x01
	envelopeVersion       = 0x01
	fieldToWayfarerID     = 0x01
	wayfarerIDBytesLen    = 32
)

var (
	ErrEnvelopePayloadTooShort    = errors.New("envelope payload too short")
	ErrEnvelopeVersionUnsupported = errors.New("envelope version unsupported")
	ErrEnvelopeFieldLengthInvalid = errors.New("envelope field length invalid")
	ErrEnvelopeRecipientNotFound  = errors.New("envelope recipient not found")
	ErrEnvelopeRecipientBadLength = errors.New("envelope recipient has invalid length")
)

// RecipientWayfarerID extracts EnvelopeV1 recipient from canonical bytes payload.
//
// Canonical bytes fixtures currently use this layout:
//   - byte[0] canonical bytes version (0x01)
//   - byte[1] envelope version (0x01)
//   - repeated fields: [field_id:1][length:4 big-endian][value:length]
//
// Field 0x01 stores recipient wayfarer bytes (32 bytes), encoded as lowercase
// hex for client-relay `send.to` comparison.
func RecipientWayfarerID(payload []byte) (string, error) {
	if len(payload) < 2 {
		return "", ErrEnvelopePayloadTooShort
	}
	if payload[0] != canonicalBytesVersion || payload[1] != envelopeVersion {
		return "", fmt.Errorf("%w: canonical=%d envelope=%d", ErrEnvelopeVersionUnsupported, payload[0], payload[1])
	}

	cursor := 2
	for cursor < len(payload) {
		if len(payload)-cursor < 5 {
			return "", ErrEnvelopeFieldLengthInvalid
		}

		fieldID := payload[cursor]
		cursor++

		fieldLength := int(binary.BigEndian.Uint32(payload[cursor : cursor+4]))
		cursor += 4

		if fieldLength < 0 || fieldLength > len(payload)-cursor {
			return "", fmt.Errorf("%w: field=%d length=%d", ErrEnvelopeFieldLengthInvalid, fieldID, fieldLength)
		}

		fieldValue := payload[cursor : cursor+fieldLength]
		cursor += fieldLength

		if fieldID != fieldToWayfarerID {
			continue
		}
		if len(fieldValue) != wayfarerIDBytesLen {
			return "", fmt.Errorf("%w: got=%d", ErrEnvelopeRecipientBadLength, len(fieldValue))
		}
		return hex.EncodeToString(fieldValue), nil
	}

	return "", ErrEnvelopeRecipientNotFound
}
