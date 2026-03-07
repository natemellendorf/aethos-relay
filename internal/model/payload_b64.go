package model

import (
	"encoding/base64"
	"errors"
	"strings"
)

// PayloadEncodingPref controls outbound payload_b64 wire encoding per connection.
type PayloadEncodingPref string

const (
	PayloadEncodingPrefBase64    PayloadEncodingPref = "base64"
	PayloadEncodingPrefBase64URL PayloadEncodingPref = "base64url"
)

var errInvalidPayloadB64 = errors.New("invalid payload_b64")

// DecodePayloadB64 decodes payload_b64 using tolerant compatibility rules.
func DecodePayloadB64(s string) ([]byte, error) {
	trimmed := trimASCIISpace(s)
	if trimmed == "" {
		return nil, errInvalidPayloadB64
	}

	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}

	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil {
			return decoded, nil
		}
	}

	return nil, errInvalidPayloadB64
}

// DetectPayloadB64EncodingPref infers preferred outbound payload_b64 encoding.
func DetectPayloadB64EncodingPref(s string) PayloadEncodingPref {
	trimmed := trimASCIISpace(s)

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

func trimASCIISpace(s string) string {
	start := 0
	end := len(s)

	for start < end && isASCIISpace(s[start]) {
		start++
	}
	for end > start && isASCIISpace(s[end-1]) {
		end--
	}

	return s[start:end]
}

func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}
