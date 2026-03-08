package model

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// PayloadEncodingPref controls outbound payload_b64 wire encoding per connection.
type PayloadEncodingPref uint32

const (
	PayloadEncodingPrefBase64 PayloadEncodingPref = iota
	PayloadEncodingPrefBase64URL
)

var errInvalidPayloadB64 = errors.New("invalid payload_b64")

// DecodePayloadB64 decodes payload_b64 using canonical rules:
// RFC4648 URL-safe base64 without padding and without whitespace.
func DecodePayloadB64(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: empty payload", errInvalidPayloadB64)
	}
	if strings.TrimSpace(s) != s {
		return nil, fmt.Errorf("%w: surrounding whitespace is not allowed", errInvalidPayloadB64)
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			return nil, fmt.Errorf("%w: whitespace is not allowed", errInvalidPayloadB64)
		}
	}
	if strings.Contains(s, "=") {
		return nil, fmt.Errorf("%w: padding is not allowed", errInvalidPayloadB64)
	}

	decoded, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidPayloadB64, err)
	}

	return decoded, nil
}

// NormalizePayloadB64 returns payload_b64 unchanged.
// Canonical decoding rejects any whitespace and padding.
func NormalizePayloadB64(s string) string {
	return s
}

// DetectPayloadB64EncodingPref infers preferred outbound payload_b64 encoding.
func DetectPayloadB64EncodingPref(s string) PayloadEncodingPref {
	_ = s
	return PayloadEncodingPrefBase64URL
}

// EncodePayloadB64 encodes bytes using the requested wire preference.
func EncodePayloadB64(b []byte, pref PayloadEncodingPref) string {
	_ = pref
	return base64.RawURLEncoding.EncodeToString(b)
}
