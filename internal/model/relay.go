package model

import (
	"math"
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

	// SCORE_WEIGHT_SUCCESS is the weight for successful delivery ratio.
	SCORE_WEIGHT_SUCCESS = 0.4

	// SCORE_WEIGHT_UPTIME is the weight for connection uptime ratio.
	SCORE_WEIGHT_UPTIME = 0.2

	// SCORE_WEIGHT_LATENCY is the weight for latency term (lower is better).
	SCORE_WEIGHT_LATENCY = 0.2

	// SCORE_WEIGHT_FORWARD is the weight for forward success rate.
	SCORE_WEIGHT_FORWARD = 0.2

	// DECAY_HALF_LIFE is the half-life for score decay (24 hours).
	DECAY_HALF_LIFE = 24 * time.Hour

	// BLACKHOLE_THRESHOLD is the number of accepted publishes without any forward/ack
	// before applying aggressive penalty.
	BLACKHOLE_THRESHOLD = 5

	// DISCONNECT_PENALTY is the score penalty per disconnect.
	DISCONNECT_PENALTY = 0.1

	// HANDSHAKE_FAILURE_PENALTY is the score penalty per handshake failure.
	HANDSHAKE_FAILURE_PENALTY = 0.15

	// MIN_SCORE is the minimum possible score.
	MIN_SCORE = 0.0

	// MAX_SCORE is the maximum possible score.
	MAX_SCORE = 1.0

	// GOOD_LATENCY_MS is the latency considered "good" for scoring (ms).
	GOOD_LATENCY_MS = 100.0

	// BAD_LATENCY_MS is the latency considered "bad" for scoring (ms).
	BAD_LATENCY_MS = 2000.0

	// MIN_PUBLISH_WIDTH is the minimum adaptive publish width.
	MIN_PUBLISH_WIDTH = 1

	// MAX_PUBLISH_WIDTH is the maximum adaptive publish width.
	MAX_PUBLISH_WIDTH = 5
)

// RelayScore holds scoring metrics for a relay.
type RelayScore struct {
	RelayID              string    `json:"relay_id"`
	SuccessfulDeliveries int       `json:"successful_deliveries"`
	FailedDeliveries     int       `json:"failed_deliveries"`
	AverageAckLatencyMs  float64   `json:"average_ack_latency_ms"`
	ConnectionUptime     float64   `json:"connection_uptime"` // ratio 0-1
	DisconnectCount      int       `json:"disconnect_count"`
	HandshakeFailures    int       `json:"handshake_failures"`
	MessagesAccepted     int       `json:"messages_accepted"`  // messages accepted but not forwarded
	MessagesForwarded    int       `json:"messages_forwarded"` // messages successfully forwarded
	LastUpdated          time.Time `json:"last_updated"`
	Score                float64   `json:"score"` // computed score 0-1
}

// NewRelayScore creates a new relay score with default values.
func NewRelayScore(relayID string) *RelayScore {
	return &RelayScore{
		RelayID:              relayID,
		SuccessfulDeliveries: 0,
		FailedDeliveries:     0,
		AverageAckLatencyMs:  0,
		ConnectionUptime:     1.0,
		DisconnectCount:      0,
		HandshakeFailures:    0,
		MessagesAccepted:     0,
		MessagesForwarded:    0,
		LastUpdated:          time.Now(),
		Score:                MAX_SCORE,
	}
}

// RecordSuccess records a successful delivery.
func (s *RelayScore) RecordSuccess(latencyMs float64) {
	s.SuccessfulDeliveries++
	s.LastUpdated = time.Now()

	// Update running average for latency
	if s.AverageAckLatencyMs == 0 {
		s.AverageAckLatencyMs = latencyMs
	} else {
		s.AverageAckLatencyMs = (s.AverageAckLatencyMs*float64(s.SuccessfulDeliveries-1) + latencyMs) / float64(s.SuccessfulDeliveries)
	}

	s.recomputeScore()
}

// RecordFailure records a failed delivery.
func (s *RelayScore) RecordFailure() {
	s.FailedDeliveries++
	s.LastUpdated = time.Now()
	s.recomputeScore()
}

// RecordDisconnect records a disconnect event.
func (s *RelayScore) RecordDisconnect() {
	s.DisconnectCount++
	s.LastUpdated = time.Now()
	s.recomputeScore()
}

// RecordHandshakeFailure records a handshake failure.
func (s *RelayScore) RecordHandshakeFailure() {
	s.HandshakeFailures++
	s.LastUpdated = time.Now()
	s.recomputeScore()
}

// RecordMessageAccepted records a message that was accepted (publish received) but not yet forwarded.
// This is used for blackhole detection.
func (s *RelayScore) RecordMessageAccepted() {
	s.MessagesAccepted++
	s.LastUpdated = time.Now()
}

// RecordMessageForwarded records a message that was successfully forwarded.
func (s *RelayScore) RecordMessageForwarded() {
	s.MessagesForwarded++
	s.LastUpdated = time.Now()
}

// IsBlackhole returns true if the relay appears to be accepting messages but not forwarding them.
func (s *RelayScore) IsBlackhole() bool {
	// If we've accepted messages but none have been forwarded
	if s.MessagesAccepted >= BLACKHOLE_THRESHOLD && s.MessagesForwarded == 0 {
		return true
	}
	// Or if accepted is significantly higher than forwarded (possible blackhole)
	if s.MessagesForwarded == 0 && s.MessagesAccepted >= BLACKHOLE_THRESHOLD {
		return true
	}
	return false
}

// recomputeScore recalculates the score based on current metrics.
func (s *RelayScore) recomputeScore() {
	var score float64

	// Success ratio (0.4 weight)
	totalDeliveries := s.SuccessfulDeliveries + s.FailedDeliveries
	var successRatio float64
	if totalDeliveries > 0 {
		successRatio = float64(s.SuccessfulDeliveries) / float64(totalDeliveries)
	} else {
		successRatio = 0.5 // Neutral if no data
	}

	// Uptime ratio (0.2 weight) - already stored as 0-1 ratio

	// Latency term (0.2 weight) - lower is better
	var latencyTerm float64
	if s.AverageAckLatencyMs > 0 {
		// Normalize: GOOD_LATENCY = 1.0, BAD_LATENCY = 0.0
		latencyTerm = 1.0 - math.Min(1.0, (s.AverageAckLatencyMs-GOOD_LATENCY_MS)/(BAD_LATENCY_MS-GOOD_LATENCY_MS))
	} else {
		latencyTerm = 0.5 // Neutral if no data
	}

	// Forward success (0.2 weight)
	var forwardSuccess float64
	if s.MessagesAccepted > 0 {
		forwardSuccess = float64(s.MessagesForwarded) / float64(s.MessagesAccepted)
	} else {
		forwardSuccess = 0.5 // Neutral if no data
	}

	// Compute base score
	score = successRatio*SCORE_WEIGHT_SUCCESS +
		s.ConnectionUptime*SCORE_WEIGHT_UPTIME +
		latencyTerm*SCORE_WEIGHT_LATENCY +
		forwardSuccess*SCORE_WEIGHT_FORWARD

	// Apply penalties for disconnects
	score -= float64(s.DisconnectCount) * DISCONNECT_PENALTY

	// Apply penalties for handshake failures
	score -= float64(s.HandshakeFailures) * HANDSHAKE_FAILURE_PENALTY

	// Apply aggressive penalty for blackhole behavior
	if s.IsBlackhole() {
		score -= 0.5 // Heavy penalty
	}

	// Clamp to valid range
	s.Score = math.Max(MIN_SCORE, math.Min(MAX_SCORE, score))
}

// ApplyDecay applies time-based decay to the score.
func (s *RelayScore) ApplyDecay() {
	if s.LastUpdated.IsZero() {
		return
	}

	elapsed := time.Since(s.LastUpdated)
	if elapsed <= 0 {
		return
	}

	// Exponential decay: score = score * 0.5^(elapsed/half_life)
	decayFactor := math.Pow(0.5, float64(elapsed)/float64(DECAY_HALF_LIFE))
	s.Score *= decayFactor

	// Also decay the metrics
	s.ConnectionUptime *= decayFactor
	if s.ConnectionUptime < 0.1 {
		s.ConnectionUptime = 0.1
	}

	s.LastUpdated = time.Now()

	// Clamp score after decay
	if s.Score < MIN_SCORE {
		s.Score = MIN_SCORE
	}
}

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
