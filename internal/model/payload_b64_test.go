package model

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecodePayloadB64_ToleratesLegacyAndCanonicalEncodings(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		expect  []byte
		wantErr bool
	}{
		{
			name:   "base64url unpadded",
			input:  "-_8",
			expect: []byte{0xfb, 0xff},
		},
		{
			name:    "base64url padded rejected",
			input:   "-_8=",
			wantErr: true,
		},
		{
			name:    "base64 padded rejected",
			input:   "+/8=",
			wantErr: true,
		},
		{
			name:    "base64 unpadded rejected",
			input:   "+/8",
			wantErr: true,
		},
		{
			name:    "whitespace rejected",
			input:   " \n\t-_8\r ",
			wantErr: true,
		},
		{
			name:    "invalid rejects",
			input:   "not_valid***",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodePayloadB64(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected decode error")
				}
				return
			}
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if !bytes.Equal(got, tt.expect) {
				t.Fatalf("decoded bytes mismatch: got %v want %v", got, tt.expect)
			}
		})
	}
}

func TestDetectPayloadB64EncodingPref(t *testing.T) {
	inputs := []string{"abc-def", "abc_def", "abc+def", "abc/def", "YWJjZA==", "YWJjZA", "YWJjZA0K"}
	for _, input := range inputs {
		if got := DetectPayloadB64EncodingPref(input); got != PayloadEncodingPrefBase64URL {
			t.Fatalf("preference mismatch for %q: got %v want %v", input, got, PayloadEncodingPrefBase64URL)
		}
	}
}

func TestEncodePayloadB64_RespectsPreference(t *testing.T) {
	payload := []byte{0xfb, 0xff}

	if got := EncodePayloadB64(payload, PayloadEncodingPrefBase64URL); got != "-_8" {
		t.Fatalf("base64url encode mismatch: got %q want %q", got, "-_8")
	}
	if got := EncodePayloadB64(payload, PayloadEncodingPrefBase64); got != "-_8" {
		t.Fatalf("base64 encode should canonicalize to base64url: got %q want %q", got, "-_8")
	}
}

func TestDecodePayloadB64_InvalidIncludesDetail(t *testing.T) {
	_, err := DecodePayloadB64("%%%")
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !errors.Is(err, errInvalidPayloadB64) {
		t.Fatalf("expected wrapped invalid payload error, got %v", err)
	}
	if err.Error() == errInvalidPayloadB64.Error() {
		t.Fatalf("expected decode detail in error, got %q", err.Error())
	}
}
