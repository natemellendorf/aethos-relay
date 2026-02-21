package model

import (
	"strings"
	"time"
)

const (
	// Frame types for relay-relay federation (not exposed to clients)
	FrameTypeRelayDescriptors   = "relay_descriptors"
	FrameTypeRelayDescriptorAck = "relay_descriptor_ack"
)

// RelayDescriptor represents a discovered relay in the mesh.
type RelayDescriptor struct {
	RelayID      string   `json:"relay_id"`
	WSURL        string   `json:"ws_url"`
	Region       string   `json:"region,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	FirstSeenAt  int64    `json:"first_seen_at"`
	LastSeenAt   int64    `json:"last_seen_at"`
	ExpiresAt    int64    `json:"expires_at"`
	AdvertisedBy string   `json:"advertised_by,omitempty"`
}

// RelayDescriptorAck represents acknowledgment of descriptor receipt.
type RelayDescriptorAck struct {
	RelayID string `json:"relay_id"`
	Status  string `json:"status"` // "accepted" or "rejected"
	Reason  string `json:"reason,omitempty"`
}

// RelayDescriptorValidationResult holds validation results.
type RelayDescriptorValidationResult struct {
	Valid       bool
	Descriptor  *RelayDescriptor
	ErrorReason string
}

// Constants for descriptor management
const (
	// MAX_DESCRIPTOR_TTL is the maximum TTL for a descriptor (~7 days)
	MAX_DESCRIPTOR_TTL = 7 * 24 * time.Hour

	// MAX_DESCRIPTORS_PER_PEER_PER_HOUR limits descriptors received from a single peer
	MAX_DESCRIPTORS_PER_PEER_PER_HOUR = 100

	// MAX_TOTAL_DESCRIPTORS is the hard cap for the descriptor registry
	MAX_TOTAL_DESCRIPTORS = 10000

	// MAX_DESCRIPTOR_PAYLOAD limits the size of descriptor messages
	MAX_DESCRIPTOR_PAYLOAD = 64 * 1024 // 64KB

	// MAX_DESCRIPTORS_PER_MESSAGE limits descriptors in a single frame
	MAX_DESCRIPTORS_PER_MESSAGE = 50

	// GOSSIP_SAMPLE_SIZE is the number of descriptors to share on gossip
	GOSSIP_SAMPLE_SIZE = 25
)

// ValidateDescriptor validates a relay descriptor.
func ValidateDescriptor(d *RelayDescriptor, now time.Time) *RelayDescriptorValidationResult {
	// Parse and validate URL
	if d.RelayID == "" {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "relay_id required",
		}
	}

	if d.WSURL == "" {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "ws_url required",
		}
	}

	// Validate URL scheme
	parsedURL := strings.ToLower(d.WSURL)
	if !strings.HasPrefix(parsedURL, "ws://") && !strings.HasPrefix(parsedURL, "wss://") {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "ws_url must use ws or wss scheme",
		}
	}

	// Validate timestamps
	if d.FirstSeenAt <= 0 {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "first_seen_at required",
		}
	}

	if d.LastSeenAt <= 0 {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "last_seen_at required",
		}
	}

	// Validate expires_at
	if d.ExpiresAt <= 0 {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "expires_at required",
		}
	}

	// Check if descriptor has already expired
	if d.ExpiresAt <= now.Unix() {
		return &RelayDescriptorValidationResult{
			Valid:       false,
			Descriptor:  d,
			ErrorReason: "descriptor already expired",
		}
	}

	// Enforce max TTL: expires_at must not exceed now + MAX_DESCRIPTOR_TTL
	maxExpiresAt := now.Add(MAX_DESCRIPTOR_TTL).Unix()
	if d.ExpiresAt > maxExpiresAt {
		// Clamp TTL to max
		d.ExpiresAt = maxExpiresAt
	}

	return &RelayDescriptorValidationResult{
		Valid:      true,
		Descriptor: d,
	}
}

// IsExpired checks if the descriptor has expired.
func (d *RelayDescriptor) IsExpired(now time.Time) bool {
	return d.ExpiresAt <= now.Unix()
}

// IsExpiringSoon checks if the descriptor will expire within the given duration.
func (d *RelayDescriptor) IsExpiringSoon(now time.Time, within time.Duration) bool {
	return d.ExpiresAt <= now.Add(within).Unix()
}
