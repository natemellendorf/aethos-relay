package model

import (
	"strings"
	"time"
)

// Constants for descriptor limits
const (
	// MAX_DESCRIPTOR_TTL is the maximum time-to-live for a descriptor (7 days).
	MAX_DESCRIPTOR_TTL = 7 * 24 * time.Hour

	// MAX_DESCRIPTORS_PER_PEER_PER_HOUR limits descriptors accepted from each peer.
	MAX_DESCRIPTORS_PER_PEER_PER_HOUR = 100

	// MAX_TOTAL_DESCRIPTORS is the maximum number of descriptors in the registry.
	MAX_TOTAL_DESCRIPTORS = 10000

	// MAX_DESCRIPTOR_PAYLOAD is the maximum size of descriptor payload (bytes).
	MAX_DESCRIPTOR_PAYLOAD = 4096
)

// RelayDescriptor represents a relay peer for auto-discovery.
type RelayDescriptor struct {
	RelayID      string    `json:"relay_id"`
	WSURL        string    `json:"ws_url"`
	Region       string    `json:"region,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	AdvertisedBy string    `json:"advertised_by"` // Peer that advertised this descriptor
}

// Validate validates a RelayDescriptor.
// Returns an error if validation fails.
func (d *RelayDescriptor) Validate() error {
	// Check required fields
	if d.RelayID == "" {
		return ErrDescriptorInvalidRelayID
	}
	if d.WSURL == "" {
		return ErrDescriptorInvalidURL
	}

	// Validate URL scheme (ws or wss)
	if !strings.HasPrefix(strings.ToLower(d.WSURL), "ws://") && !strings.HasPrefix(strings.ToLower(d.WSURL), "wss://") {
		return ErrDescriptorInvalidURLScheme
	}

	// Validate timestamps
	if d.FirstSeenAt.IsZero() {
		return ErrDescriptorInvalidTimestamp
	}
	if d.LastSeenAt.IsZero() {
		return ErrDescriptorInvalidTimestamp
	}
	if d.ExpiresAt.IsZero() {
		return ErrDescriptorInvalidTimestamp
	}
	if d.LastSeenAt.Before(d.FirstSeenAt) {
		return ErrDescriptorInvalidTimestamp
	}
	if d.ExpiresAt.Before(d.LastSeenAt) {
		return ErrDescriptorInvalidTimestamp
	}

	// Clamp TTL to MAX_DESCRIPTOR_TTL
	if d.ExpiresAt.Sub(d.LastSeenAt) > MAX_DESCRIPTOR_TTL {
		d.ExpiresAt = d.LastSeenAt.Add(MAX_DESCRIPTOR_TTL)
	}

	return nil
}

// IsExpired returns true if the descriptor has expired.
func (d *RelayDescriptor) IsExpired() bool {
	return time.Now().After(d.ExpiresAt)
}

// IsStale returns true if the descriptor hasn't been seen recently.
func (d *RelayDescriptor) IsStale(staleness time.Duration) bool {
	return time.Since(d.LastSeenAt) > staleness
}

// Descriptor validation errors
var (
	ErrDescriptorInvalidRelayID   = &DescriptorError{"relay_id is required"}
	ErrDescriptorInvalidURL       = &DescriptorError{"ws_url is required"}
	ErrDescriptorInvalidURLScheme = &DescriptorError{"ws_url must use ws:// or wss:// scheme"}
	ErrDescriptorInvalidTimestamp = &DescriptorError{"invalid timestamp"}
	ErrDescriptorRateLimited      = &DescriptorError{"rate limited by peer"}
	ErrDescriptorRegistryFull     = &DescriptorError{"descriptor registry full"}
)

// DescriptorError represents a descriptor validation error.
type DescriptorError struct {
	Message string
}

func (e *DescriptorError) Error() string {
	return e.Message
}

// RelayDescriptorFrame carries relay descriptors between peers.
type RelayDescriptorFrame struct {
	Type        string            `json:"type"`
	Descriptors []RelayDescriptor `json:"descriptors"`
}

// RelayDescriptorAckFrame is an optional acknowledgment for relay descriptors.
type RelayDescriptorAckFrame struct {
	Type     string   `json:"type"`
	RelayIDs []string `json:"relay_ids"`          // Successfully processed relay IDs
	Rejected []string `json:"rejected,omitempty"` // Rejected relay IDs with reasons could be added
}

// Frame types for relay discovery
const (
	FrameTypeRelayDescriptors   = "relay_descriptors"
	FrameTypeRelayDescriptorAck = "relay_descriptor_ack"
)

// IsRelayDescriptorFrameType checks if a frame type is for relay discovery.
func IsRelayDescriptorFrameType(frameType string) bool {
	switch frameType {
	case FrameTypeRelayDescriptors, FrameTypeRelayDescriptorAck:
		return true
	default:
		return false
	}
}
