package model

import (
	"testing"
	"time"
)

func TestDescriptorValidation(t *testing.T) {
	now := time.Now()

	t.Run("valid_descriptor", func(t *testing.T) {
		desc := &RelayDescriptor{
			RelayID:      "relay-123",
			WSURL:        "wss://relay.example.com:8080/ws",
			Region:       "us-east-1",
			Tags:         []string{"production"},
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
			AdvertisedBy: "peer-1",
		}
		if err := desc.Validate(); err != nil {
			t.Errorf("expected valid descriptor, got error: %v", err)
		}
	})

	t.Run("descriptor_validation_rejects_bad_url", func(t *testing.T) {
		tests := []struct {
			name string
			desc *RelayDescriptor
		}{
			{
				name: "empty_relay_id",
				desc: &RelayDescriptor{
					RelayID:     "",
					WSURL:       "wss://relay.example.com:8080/ws",
					FirstSeenAt: now,
					LastSeenAt:  now,
					ExpiresAt:   now.Add(24 * time.Hour),
				},
			},
			{
				name: "empty_ws_url",
				desc: &RelayDescriptor{
					RelayID:     "relay-123",
					WSURL:       "",
					FirstSeenAt: now,
					LastSeenAt:  now,
					ExpiresAt:   now.Add(24 * time.Hour),
				},
			},
			{
				name: "http_url_not_ws",
				desc: &RelayDescriptor{
					RelayID:     "relay-123",
					WSURL:       "https://relay.example.com:8080/ws",
					FirstSeenAt: now,
					LastSeenAt:  now,
					ExpiresAt:   now.Add(24 * time.Hour),
				},
			},
			{
				name: "invalid_url_scheme",
				desc: &RelayDescriptor{
					RelayID:     "relay-123",
					WSURL:       "ftp://relay.example.com:8080",
					FirstSeenAt: now,
					LastSeenAt:  now,
					ExpiresAt:   now.Add(24 * time.Hour),
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.desc.Validate()
				if err == nil {
					t.Errorf("expected validation error for %s", tt.name)
				}
			})
		}
	})

	t.Run("descriptor_validation_clamps_ttl", func(t *testing.T) {
		// Create descriptor with TTL exceeding MAX_DESCRIPTOR_TTL
		desc := &RelayDescriptor{
			RelayID:      "relay-123",
			WSURL:        "wss://relay.example.com:8080/ws",
			FirstSeenAt:  now,
			LastSeenAt:   now,
			ExpiresAt:    now.Add(30 * 24 * time.Hour), // 30 days > MAX_DESCRIPTOR_TTL (7 days)
			AdvertisedBy: "peer-1",
		}

		// Validate should clamp TTL
		err := desc.Validate()
		if err != nil {
			t.Errorf("unexpected validation error: %v", err)
		}

		// Check that TTL was clamped to MAX_DESCRIPTOR_TTL
		maxTTL := now.Add(MAX_DESCRIPTOR_TTL)
		if desc.ExpiresAt.After(maxTTL) {
			t.Errorf("TTL not clamped: expires_at = %v, expected <= %v", desc.ExpiresAt, maxTTL)
		}
	})
}

func TestRelayDescriptorFrameTypes(t *testing.T) {
	t.Run("is_relay_descriptor_frame_type", func(t *testing.T) {
		tests := []struct {
			frameType string
			expected  bool
		}{
			{FrameTypeRelayDescriptors, true},
			{FrameTypeRelayDescriptorAck, true},
			{"relay_hello", false},
			{"message", false},
			{"", false},
		}

		for _, tt := range tests {
			result := IsRelayDescriptorFrameType(tt.frameType)
			if result != tt.expected {
				t.Errorf("IsRelayDescriptorFrameType(%q) = %v, expected %v", tt.frameType, result, tt.expected)
			}
		}
	})
}

func TestRelayDescriptorIsExpired(t *testing.T) {
	now := time.Now()

	t.Run("expired_descriptor", func(t *testing.T) {
		desc := &RelayDescriptor{
			RelayID:     "relay-123",
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now.Add(-24 * time.Hour),
			LastSeenAt:  now.Add(-24 * time.Hour),
			ExpiresAt:   now.Add(-1 * time.Hour), // Expired 1 hour ago
		}

		if !desc.IsExpired() {
			t.Error("expected descriptor to be expired")
		}
	})

	t.Run("valid_descriptor", func(t *testing.T) {
		desc := &RelayDescriptor{
			RelayID:     "relay-123",
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now,
			LastSeenAt:  now,
			ExpiresAt:   now.Add(24 * time.Hour),
		}

		if desc.IsExpired() {
			t.Error("expected descriptor to not be expired")
		}
	})
}

func TestRelayDescriptorIsStale(t *testing.T) {
	now := time.Now()

	t.Run("stale_descriptor", func(t *testing.T) {
		desc := &RelayDescriptor{
			RelayID:     "relay-123",
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now.Add(-2 * time.Hour),
			LastSeenAt:  now.Add(-2 * time.Hour),
			ExpiresAt:   now.Add(24 * time.Hour),
		}

		if !desc.IsStale(1 * time.Hour) {
			t.Error("expected descriptor to be stale with 1 hour threshold")
		}
	})

	t.Run("fresh_descriptor", func(t *testing.T) {
		desc := &RelayDescriptor{
			RelayID:     "relay-123",
			WSURL:       "wss://relay.example.com:8080/ws",
			FirstSeenAt: now,
			LastSeenAt:  now,
			ExpiresAt:   now.Add(24 * time.Hour),
		}

		if desc.IsStale(1 * time.Hour) {
			t.Error("expected descriptor to not be stale with 1 hour threshold")
		}
	})
}
