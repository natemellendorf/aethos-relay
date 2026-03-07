package model

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// PayloadEncodingPref controls outbound payload_b64 wire encoding per connection.
type PayloadEncodingPref uint32

const (
	PayloadEncodingPrefBase64 PayloadEncodingPref = iota
	PayloadEncodingPrefBase64URL
)

var errInvalidPayloadB64 = errors.New("invalid payload_b64")

// DecodePayloadB64 decodes payload_b64 using tolerant compatibility rules.
func DecodePayloadB64(s string) ([]byte, error) {
	trimmed := NormalizePayloadB64(s)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty payload", errInvalidPayloadB64)
	}

	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}

	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("%w: %v", errInvalidPayloadB64, lastErr)
}

// NormalizePayloadB64 trims surrounding ASCII whitespace from payload_b64.
func NormalizePayloadB64(s string) string {
	return strings.TrimSpace(s)
}

// DetectPayloadB64EncodingPref infers preferred outbound payload_b64 encoding.
func DetectPayloadB64EncodingPref(s string) PayloadEncodingPref {
	trimmed := NormalizePayloadB64(s)

	if strings.ContainsAny(trimmed, "-_") {
		return PayloadEncodingPrefBase64URL
	}
	if strings.ContainsAny(trimmed, "+/") {
		return PayloadEncodingPrefBase64
	}
	if strings.Contains(trimmed, "=") {
		return PayloadEncodingPrefBase64
	}
	if len(trimmed)%4 != 0 {
		return PayloadEncodingPrefBase64URL
	}
	return PayloadEncodingPrefBase64
}

// EncodePayloadB64 encodes bytes using the requested wire preference.
func EncodePayloadB64(b []byte, pref PayloadEncodingPref) string {
	if pref == PayloadEncodingPrefBase64URL {
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return base64.StdEncoding.EncodeToString(b)
}
