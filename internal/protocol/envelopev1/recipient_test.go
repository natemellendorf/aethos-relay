package envelopev1

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestRecipientWayfarerID_ExtractsCanonicalRecipient(t *testing.T) {
	payloadB64 := "AQEBAAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4fAgAAABNtYW5pZmVzdC1maXhlZC0wMDAxAwAAAAxoZWxsby1hZXRob3M"
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("decode fixture payload: %v", err)
	}

	got, err := RecipientWayfarerID(payload)
	if err != nil {
		t.Fatalf("extract recipient: %v", err)
	}
	want := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	if got != want {
		t.Fatalf("recipient mismatch: got %q want %q", got, want)
	}
}

func TestRecipientWayfarerID_RejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr error
	}{
		{
			name:    "short",
			payload: []byte{0x01},
			wantErr: ErrEnvelopePayloadTooShort,
		},
		{
			name:    "wrong version",
			payload: []byte{0x02, 0x01},
			wantErr: ErrEnvelopeVersionUnsupported,
		},
		{
			name: "missing recipient",
			payload: []byte{
				0x01, 0x01,
				0x02, 0x00, 0x00, 0x00, 0x01, 0xaa,
			},
			wantErr: ErrEnvelopeRecipientNotFound,
		},
		{
			name: "recipient wrong length",
			payload: []byte{
				0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, 0x1f,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			wantErr: ErrEnvelopeRecipientBadLength,
		},
		{
			name: "field length exceeds payload",
			payload: []byte{
				0x01, 0x01,
				0x01, 0x00, 0x00, 0x00, 0x20,
				0x00,
			},
			wantErr: ErrEnvelopeFieldLengthInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RecipientWayfarerID(tt.payload)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error mismatch: got %v want %v", err, tt.wantErr)
			}
		})
	}
}
