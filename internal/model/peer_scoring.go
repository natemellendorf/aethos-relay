package model

import (
	"math"
	"math/rand"
	"sort"
	"time"
)

// PeerScoreConstants defines constants for peer scoring.
const (
	// PeerScoreWeightSuccess is the weight for successful delivery ratio.
	PeerScoreWeightSuccess = 0.35

	// PeerScoreWeightLatency is the weight for latency term (lower is better).
	PeerScoreWeightLatency = 0.25

	// PeerScoreWeightStability is the weight for stability (consecutive successes).
	PeerScoreWeightStability = 0.20

	// PeerScoreWeightUptime is the weight for connection uptime.
	PeerScoreWeightUptime = 0.20

	// PeerScoreDecayHalfLife is the half-life for score decay (1 hour).
	PeerScoreDecayHalfLife = 1 * time.Hour

	// PeerScoreMin is the minimum possible score.
	PeerScoreMin = 0.0

	// PeerScoreMax is the maximum possible score.
	PeerScoreMax = 1.0

	// GoodAckLatencyMs is the latency considered "good" for scoring (ms).
	GoodAckLatencyMs = 500.0

	// BadAckLatencyMs is the latency considered "bad" for scoring (ms).
	BadAckLatencyMs = 5000.0

	// DecayThreshold is the time without successful acks before decay applies.
	DecayThreshold = 5 * time.Minute

	// EWMAFactor is the exponential weighted moving average factor for latency.
	EWMAFactor = 0.3
)

// PeerMetrics holds metrics for a connected federation peer.
type PeerMetrics struct {
	PeerID              string    `json:"peer_id"`
	Connected           bool      `json:"connected"`
	LastConnectedAt     time.Time `json:"last_connected_at,omitempty"`
	LastDisconnectedAt  time.Time `json:"last_disconnected_at,omitempty"`
	ForwardsTotal       int       `json:"forwards_total"`
	AcksTotal           int       `json:"acks_total"`
	AckLatencyEWMA      float64   `json:"ack_latency_ewma"`
	TimeoutsTotal       int       `json:"timeouts_total"`
	FailuresTotal       int       `json:"failures_total"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastForwardAt       time.Time `json:"last_forward_at,omitempty"`
	LastAckAt           time.Time `json:"last_ack_at,omitempty"`
	BackoffUntil        time.Time `json:"backoff_until,omitempty"`
	Score               float64   `json:"score"`
	LastScoreUpdate     time.Time `json:"last_score_update"`
	Seed                int64     `json:"seed"` // For deterministic exploration selection
}

// NewPeerMetrics creates new peer metrics with defaults.
func NewPeerMetrics(peerID string) *PeerMetrics {
	return &PeerMetrics{
		PeerID:          peerID,
		Connected:       true,
		LastConnectedAt: time.Now(),
		Score:           PeerScoreMax,
		Seed:            rand.Int63(),
	}
}

// RecordConnect records a connection event.
func (m *PeerMetrics) RecordConnect() {
	m.Connected = true
	m.LastConnectedAt = time.Now()
}

// RecordDisconnect records a disconnection event.
func (m *PeerMetrics) RecordDisconnect() {
	m.Connected = false
	m.LastDisconnectedAt = time.Now()
}

// RecordForward records a forwarded message.
func (m *PeerMetrics) RecordForward() {
	m.ForwardsTotal++
	m.LastForwardAt = time.Now()
}

// RecordAck records a successful ack and updates latency EWMA.
func (m *PeerMetrics) RecordAck(ackLatencyMs float64) {
	m.AcksTotal++
	m.LastAckAt = time.Now()
	m.ConsecutiveFailures = 0

	// Update EWMA
	if m.AckLatencyEWMA == 0 {
		m.AckLatencyEWMA = ackLatencyMs
	} else {
		m.AckLatencyEWMA = EWMAFactor*ackLatencyMs + (1-EWMAFactor)*m.AckLatencyEWMA
	}

	m.recomputeScore()
}

// RecordTimeout records a timeout event.
func (m *PeerMetrics) RecordTimeout() {
	m.TimeoutsTotal++
	m.ConsecutiveFailures++
	m.recomputeScore()
}

// RecordFailure records a failure event.
func (m *PeerMetrics) RecordFailure() {
	m.FailuresTotal++
	m.ConsecutiveFailures++
	m.recomputeScore()
}

// ApplyBackoff applies exponential backoff after failures.
func (m *PeerMetrics) ApplyBackoff(baseDelay, maxDelay time.Duration) {
	// Exponential backoff: 2^consecutive_failures * base, capped at maxDelay
	backoff := baseDelay * time.Duration(math.Pow(2, float64(m.ConsecutiveFailures)))
	if backoff > maxDelay {
		backoff = maxDelay
	}
	m.BackoffUntil = time.Now().Add(backoff)
}

// ClearBackoff clears the backoff timer after successful operation.
func (m *PeerMetrics) ClearBackoff() {
	m.BackoffUntil = time.Time{}
}

// IsBackingOff returns true if the peer is currently backing off.
func (m *PeerMetrics) IsBackingOff() bool {
	return !m.BackoffUntil.IsZero() && time.Now().Before(m.BackoffUntil)
}

// IsHealthy returns true if the peer is considered healthy.
func (m *PeerMetrics) IsHealthy() bool {
	return m.Connected && m.Score >= 0.3 && !m.IsBackingOff()
}

// recomputeScore recalculates the peer score based on current metrics.
func (m *PeerMetrics) recomputeScore() {
	var score float64

	// Success rate (0.35 weight)
	totalAttempts := m.AcksTotal + m.TimeoutsTotal + m.FailuresTotal
	var successRate float64
	if totalAttempts > 0 {
		successRate = float64(m.AcksTotal) / float64(totalAttempts)
	} else {
		successRate = 0.5 // Neutral if no data
	}

	// Latency term (0.25 weight) - lower is better
	var latencyTerm float64
	if m.AckLatencyEWMA > 0 {
		// Normalize: GoodLatency = 1.0, BadLatency = 0.0
		latencyTerm = 1.0 - math.Min(1.0, (m.AckLatencyEWMA-GoodAckLatencyMs)/(BadAckLatencyMs-GoodAckLatencyMs))
	} else {
		latencyTerm = 0.5 // Neutral if no data
	}

	// Stability bonus (0.20 weight) - more consecutive successes is better
	var stabilityTerm float64
	if m.ConsecutiveFailures == 0 {
		stabilityTerm = 1.0
	} else {
		// Decay stability with consecutive failures
		stabilityTerm = 1.0 / (1.0 + float64(m.ConsecutiveFailures))
	}

	// Uptime term (0.20 weight)
	var uptimeTerm float64
	if m.LastConnectedAt.IsZero() {
		uptimeTerm = 0.5
	} else {
		// Calculate uptime ratio based on time since first connect
		uptime := time.Since(m.LastConnectedAt)
		// If we've been connected for more than 1 minute, assume full uptime
		if uptime > time.Minute {
			uptimeTerm = 1.0
		} else {
			uptimeTerm = uptime.Minutes()
		}
	}

	// Compute base score
	score = successRate*PeerScoreWeightSuccess +
		latencyTerm*PeerScoreWeightLatency +
		stabilityTerm*PeerScoreWeightStability +
		uptimeTerm*PeerScoreWeightUptime

	// Apply failure penalties
	score -= float64(m.ConsecutiveFailures) * 0.05
	score -= float64(m.TimeoutsTotal) * 0.02
	score -= float64(m.FailuresTotal) * 0.03

	// Clamp to valid range
	m.Score = math.Max(PeerScoreMin, math.Min(PeerScoreMax, score))
	m.LastScoreUpdate = time.Now()
}

// ApplyDecay applies time-based decay if no successful acks recently.
func (m *PeerMetrics) ApplyDecay() {
	if m.LastAckAt.IsZero() {
		return
	}

	timeSinceLastAck := time.Since(m.LastAckAt)
	if timeSinceLastAck > DecayThreshold {
		// Exponential decay: score = score * 0.5^(elapsed/half_life)
		decayFactor := math.Pow(0.5, float64(timeSinceLastAck)/float64(PeerScoreDecayHalfLife))
		m.Score *= decayFactor

		// Clamp score after decay
		if m.Score < PeerScoreMin {
			m.Score = PeerScoreMin
		}
		m.LastScoreUpdate = time.Now()
	}
}

// PeerMetricsSlice is a slice of PeerMetrics for sorting.
type PeerMetricsSlice []*PeerMetrics

// Len returns the length of the slice.
func (p PeerMetricsSlice) Len() int {
	return len(p)
}

// Less returns true if i's score is less than j's score.
func (p PeerMetricsSlice) Less(i, j int) bool {
	return p[i].Score < p[j].Score
}

// Swap swaps elements i and j.
func (p PeerMetricsSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// SortByPeerScore sorts peers by score in descending order (highest first).
func SortByPeerScore(peers []*PeerMetrics) {
	sort.Sort(sort.Reverse(PeerMetricsSlice(peers)))
}

// PeerScoreResponse is the JSON response for peer metrics.
type PeerScoreResponse struct {
	PeerID              string    `json:"peer_id"`
	Connected           bool      `json:"connected"`
	LastConnectedAt     time.Time `json:"last_connected_at,omitempty"`
	LastDisconnectedAt  time.Time `json:"last_disconnected_at,omitempty"`
	ForwardsTotal       int       `json:"forwards_total"`
	AcksTotal           int       `json:"acks_total"`
	AckLatencyEWMA      float64   `json:"ack_latency_ewma"`
	TimeoutsTotal       int       `json:"timeouts_total"`
	FailuresTotal       int       `json:"failures_total"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastForwardAt       time.Time `json:"last_forward_at,omitempty"`
	LastAckAt           time.Time `json:"last_ack_at,omitempty"`
	BackoffUntil        time.Time `json:"backoff_until,omitempty"`
	Score               float64   `json:"score"`
	IsHealthy           bool      `json:"is_healthy"`
	IsBackingOff        bool      `json:"is_backing_off"`
}

// ToResponse converts PeerMetrics to PeerScoreResponse.
func (m *PeerMetrics) ToResponse() *PeerScoreResponse {
	return &PeerScoreResponse{
		PeerID:              m.PeerID,
		Connected:           m.Connected,
		LastConnectedAt:     m.LastConnectedAt,
		LastDisconnectedAt:  m.LastDisconnectedAt,
		ForwardsTotal:       m.ForwardsTotal,
		AcksTotal:           m.AcksTotal,
		AckLatencyEWMA:      m.AckLatencyEWMA,
		TimeoutsTotal:       m.TimeoutsTotal,
		FailuresTotal:       m.FailuresTotal,
		ConsecutiveFailures: m.ConsecutiveFailures,
		LastForwardAt:       m.LastForwardAt,
		LastAckAt:           m.LastAckAt,
		BackoffUntil:        m.BackoffUntil,
		Score:               m.Score,
		IsHealthy:           m.IsHealthy(),
		IsBackingOff:        m.IsBackingOff(),
	}
}
