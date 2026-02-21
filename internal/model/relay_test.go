package model

import (
	"testing"
	"time"
)

func TestDescriptorValidationRejectsBadURL(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name       string
		descriptor *RelayDescriptor
		wantValid  bool
		wantReason string
	}{
		{
			name: "empty relay_id",
			descriptor: &RelayDescriptor{
				RelayID:     "",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "relay_id required",
		},
		{
			name: "empty ws_url",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "ws_url required",
		},
		{
			name: "invalid scheme http",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "http://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "ws_url must use ws or wss scheme",
		},
		{
			name: "invalid scheme https",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "https://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "ws_url must use ws or wss scheme",
		},
		{
			name: "already expired",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: now.Add(-24 * time.Hour).Unix(),
				LastSeenAt:  now.Add(-24 * time.Hour).Unix(),
				ExpiresAt:   now.Add(-1 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "descriptor already expired",
		},
		{
			name: "missing first_seen_at",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: 0,
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "first_seen_at required",
		},
		{
			name: "missing last_seen_at",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  0,
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid:  false,
			wantReason: "last_seen_at required",
		},
		{
			name: "missing expires_at",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   0,
			},
			wantValid:  false,
			wantReason: "expires_at required",
		},
		{
			name: "valid wss URL",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-1",
				WSURL:       "wss://relay.example.com:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid: true,
		},
		{
			name: "valid ws URL (local dev)",
			descriptor: &RelayDescriptor{
				RelayID:     "relay-2",
				WSURL:       "ws://localhost:8080/ws",
				FirstSeenAt: now.Unix(),
				LastSeenAt:  now.Unix(),
				ExpiresAt:   now.Add(24 * time.Hour).Unix(),
			},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateDescriptor(tt.descriptor, now)
			if result.Valid != tt.wantValid {
				t.Errorf("ValidateDescriptor() valid = %v, want %v", result.Valid, tt.wantValid)
			}
			if !tt.wantValid && result.ErrorReason != tt.wantReason {
				t.Errorf("ValidateDescriptor() reason = %v, want %v", result.ErrorReason, tt.wantReason)
			}
		})
	}
}

func TestDescriptorValidationClampsTTL(t *testing.T) {
	now := time.Now()

	// Create descriptor with TTL exceeding MAX_DESCRIPTOR_TTL
	descriptor := &RelayDescriptor{
		RelayID:     "relay-1",
		WSURL:       "wss://relay.example.com:8080/ws",
		FirstSeenAt: now.Unix(),
		LastSeenAt:  now.Unix(),
		ExpiresAt:   now.Add(30 * 24 * time.Hour).Unix(), // 30 days - exceeds max
	}

	result := ValidateDescriptor(descriptor, now)

	if !result.Valid {
		t.Fatalf("ValidateDescriptor() valid = false, want true")
	}

	// TTL should be clamped to MAX_DESCRIPTOR_TTL
	maxExpiresAt := now.Add(MAX_DESCRIPTOR_TTL).Unix()
	if result.Descriptor.ExpiresAt > maxExpiresAt {
		t.Errorf("TTL not clamped: expires_at = %d, max should be %d", result.Descriptor.ExpiresAt, maxExpiresAt)
	}

	if result.Descriptor.ExpiresAt < now.Unix() {
		t.Error("Clamped TTL should not be in the past")
	}
}

func TestDescriptorIsExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{
			name:      "not expired",
			expiresAt: now.Add(1 * time.Hour).Unix(),
			want:      false,
		},
		{
			name:      "just expired",
			expiresAt: now.Add(-1 * time.Minute).Unix(),
			want:      true,
		},
		{
			name:      "expired long ago",
			expiresAt: now.Add(-24 * time.Hour).Unix(),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &RelayDescriptor{
				RelayID:   "relay-1",
				ExpiresAt: tt.expiresAt,
			}
			if got := d.IsExpired(now); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDescriptorIsExpiringSoon(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		expires int64
		within  time.Duration
		want    bool
	}{
		{
			name:    "not expiring soon",
			expires: now.Add(2 * time.Hour).Unix(),
			within:  1 * time.Hour,
			want:    false,
		},
		{
			name:    "expiring soon",
			expires: now.Add(30 * time.Minute).Unix(),
			within:  1 * time.Hour,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &RelayDescriptor{
				RelayID:   "relay-1",
				ExpiresAt: tt.expires,
			}
			if got := d.IsExpiringSoon(now, tt.within); got != tt.want {
				t.Errorf("IsExpiringSoon() = %v, want %v", got, tt.want)
			}
		})
	}
}
