package model

import (
	"testing"
	"time"
)

func TestEnvelope_Validate(t *testing.T) {
	tests := []struct {
		name    string
		env     *Envelope
		wantErr bool
	}{
		{
			name: "valid envelope",
			env: &Envelope{
				ID:              "env-1",
				DestinationID:   "dest-1",
				OpaquePayload:   []byte("test"),
				OriginRelayID:   "relay-1",
				CurrentHopCount: 0,
				CreatedAt:       time.Now(),
				ExpiresAt:       time.Now().Add(24 * time.Hour),
			},
			wantErr: false,
		},
		{
			name: "missing ID",
			env: &Envelope{
				DestinationID:   "dest-1",
				OpaquePayload:   []byte("test"),
				OriginRelayID:   "relay-1",
				CurrentHopCount: 0,
				CreatedAt:       time.Now(),
				ExpiresAt:       time.Now().Add(24 * time.Hour),
			},
			wantErr: true,
		},
		{
			name: "missing destination",
			env: &Envelope{
				ID:              "env-1",
				OpaquePayload:   []byte("test"),
				OriginRelayID:   "relay-1",
				CurrentHopCount: 0,
				CreatedAt:       time.Now(),
				ExpiresAt:       time.Now().Add(24 * time.Hour),
			},
			wantErr: true,
		},
		{
			name: "expires before created",
			env: &Envelope{
				ID:              "env-1",
				DestinationID:   "dest-1",
				OpaquePayload:   []byte("test"),
				OriginRelayID:   "relay-1",
				CurrentHopCount: 0,
				CreatedAt:       time.Now(),
				ExpiresAt:       time.Now().Add(-1 * time.Hour),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.env.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Envelope.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnvelope_IsExpired(t *testing.T) {
	tests := []struct {
		name     string
		env      *Envelope
		expected bool
	}{
		{
			name: "not expired",
			env: &Envelope{
				ExpiresAt: time.Now().Add(24 * time.Hour),
			},
			expected: false,
		},
		{
			name: "expired",
			env: &Envelope{
				ExpiresAt: time.Now().Add(-1 * time.Hour),
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsExpired(); got != tt.expected {
				t.Errorf("Envelope.IsExpired() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestEnvelopeCodec_EncodeDecode(t *testing.T) {
	codec := &EnvelopeCodec{}
	original := &Envelope{
		ID:              "test-envelope-1",
		DestinationID:   "recipient-123",
		OpaquePayload:   []byte("test payload data"),
		OriginRelayID:   "relay-1",
		CurrentHopCount: 3,
		CreatedAt:       time.Now().Truncate(time.Nanosecond),
		ExpiresAt:       time.Now().Add(24 * time.Hour).Truncate(time.Nanosecond),
	}

	// Encode
	encoded, err := codec.EncodeEnvelope(original)
	if err != nil {
		t.Fatalf("failed to encode envelope: %v", err)
	}

	// Decode
	decoded, err := codec.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("failed to decode envelope: %v", err)
	}

	// Compare
	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %s, want %s", decoded.ID, original.ID)
	}
	if decoded.DestinationID != original.DestinationID {
		t.Errorf("DestinationID mismatch: got %s, want %s", decoded.DestinationID, original.DestinationID)
	}
	if string(decoded.OpaquePayload) != string(original.OpaquePayload) {
		t.Errorf("Payload mismatch: got %s, want %s", decoded.OpaquePayload, original.OpaquePayload)
	}
	if decoded.CurrentHopCount != original.CurrentHopCount {
		t.Errorf("Hop count mismatch: got %d, want %d", decoded.CurrentHopCount, original.CurrentHopCount)
	}
}

func TestConstants(t *testing.T) {
	// Verify constants are set
	if MAX_HOPS <= 0 {
		t.Error("MAX_HOPS should be positive")
	}
	if MAX_ENVELOPE_SIZE <= 0 {
		t.Error("MAX_ENVELOPE_SIZE should be positive")
	}
	if MAX_TTL_SECONDS <= 0 {
		t.Error("MAX_TTL_SECONDS should be positive")
	}
	if MAX_FEDERATION_PEERS <= 0 {
		t.Error("MAX_FEDERATION_PEERS should be positive")
	}
	if DEFAULT_TTL_SECONDS <= 0 {
		t.Error("DEFAULT_TTL_SECONDS should be positive")
	}
	if DEFAULT_TTL_SECONDS > MAX_TTL_SECONDS {
		t.Error("DEFAULT_TTL_SECONDS should not exceed MAX_TTL_SECONDS")
	}
}
