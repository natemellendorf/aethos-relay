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
			name:   "base64url padded",
			input:  "-_8=",
			expect: []byte{0xfb, 0xff},
		},
		{
			name:   "base64 padded",
			input:  "+/8=",
			expect: []byte{0xfb, 0xff},
		},
		{
			name:   "base64 unpadded",
			input:  "+/8",
			expect: []byte{0xfb, 0xff},
		},
		{
			name:   "trims ascii whitespace",
			input:  " \n\t+/8=\r ",
			expect: []byte{0xfb, 0xff},
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
	tests := []struct {
		name  string
		input string
		want  PayloadEncodingPref
	}{
		{name: "dash implies url", input: "abc-def", want: PayloadEncodingPrefBase64URL},
		{name: "underscore implies url", input: "abc_def", want: PayloadEncodingPrefBase64URL},
		{name: "plus implies std", input: "abc+def", want: PayloadEncodingPrefBase64},
		{name: "slash implies std", input: "abc/def", want: PayloadEncodingPrefBase64},
		{name: "padding implies std", input: "YWJjZA==", want: PayloadEncodingPrefBase64},
		{name: "unpadded len mod 4 implies url", input: "YWJjZA", want: PayloadEncodingPrefBase64URL},
		{name: "ambiguous defaults std", input: "YWJjZA0K", want: PayloadEncodingPrefBase64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectPayloadB64EncodingPref(tt.input); got != tt.want {
				t.Fatalf("preference mismatch: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestEncodePayloadB64_RespectsPreference(t *testing.T) {
	payload := []byte{0xfb, 0xff}

	if got := EncodePayloadB64(payload, PayloadEncodingPrefBase64URL); got != "-_8" {
		t.Fatalf("base64url encode mismatch: got %q want %q", got, "-_8")
	}
	if got := EncodePayloadB64(payload, PayloadEncodingPrefBase64); got != "+/8=" {
		t.Fatalf("base64 encode mismatch: got %q want %q", got, "+/8=")
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
