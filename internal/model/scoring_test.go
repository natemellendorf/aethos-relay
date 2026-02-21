package model

import (
	"testing"
	"time"
)

func TestRelayScoreRecordSuccess(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Record some successes
	score.RecordSuccess(50.0) // 50ms latency
	score.RecordSuccess(100.0)
	score.RecordSuccess(75.0)

	if score.SuccessfulDeliveries != 3 {
		t.Errorf("expected 3 successful deliveries, got %d", score.SuccessfulDeliveries)
	}

	// Score should be high (close to 1.0)
	if score.Score < 0.7 {
		t.Errorf("expected score >= 0.7 after successes, got %f", score.Score)
	}

	// Average latency should be around 75ms
	if score.AverageAckLatencyMs < 70 || score.AverageAckLatencyMs > 80 {
		t.Errorf("expected average latency around 75ms, got %f", score.AverageAckLatencyMs)
	}
}

func TestRelayScoreRecordFailure(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Record initial success to set a baseline
	initialScore := score.Score

	// Record failures
	score.RecordFailure()
	score.RecordFailure()

	if score.FailedDeliveries != 2 {
		t.Errorf("expected 2 failed deliveries, got %d", score.FailedDeliveries)
	}

	// Score should decrease
	if score.Score >= initialScore {
		t.Errorf("expected score to decrease after failures, got %f (was %f)", score.Score, initialScore)
	}
}

func TestRelayScoreDisconnectPenalty(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Record disconnects
	score.RecordDisconnect()
	score.RecordDisconnect()
	score.RecordDisconnect()

	// Score should have penalty applied
	expectedPenalty := 3 * DISCONNECT_PENALTY
	if score.Score > MAX_SCORE-expectedPenalty+0.01 {
		t.Errorf("expected score penalty for disconnects, got %f", score.Score)
	}
}

func TestRelayScoreHandshakeFailurePenalty(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Record handshake failures
	score.RecordHandshakeFailure()
	score.RecordHandshakeFailure()

	// Score should have penalty applied
	expectedPenalty := 2 * HANDSHAKE_FAILURE_PENALTY
	if score.Score > MAX_SCORE-expectedPenalty+0.01 {
		t.Errorf("expected score penalty for handshake failures, got %f", score.Score)
	}
}

func TestRelayScoreBlackholeDetection(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Record messages accepted but not forwarded
	for i := 0; i < BLACKHOLE_THRESHOLD; i++ {
		score.RecordMessageAccepted()
	}

	// Should be detected as blackhole
	if !score.IsBlackhole() {
		t.Error("expected blackhole detection after threshold reached")
	}

	// Now record some forwards
	score.RecordMessageForwarded()
	score.RecordMessageForwarded()

	// Should no longer be blackhole
	if score.IsBlackhole() {
		t.Error("expected no longer blackhole after forwards recorded")
	}
}

func TestRelayScoreDecay(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Set a high score
	score.Score = 0.9

	// Manually set last updated to 25 hours ago (past half-life)
	score.LastUpdated = time.Now().Add(-25 * time.Hour)

	// Apply decay
	score.ApplyDecay()

	// Score should be approximately half (0.9 * 0.5 = 0.45)
	if score.Score > 0.5 || score.Score < 0.4 {
		t.Errorf("expected score around 0.45 after decay, got %f", score.Score)
	}
}

func TestRelayScoreMultipleFactors(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Mix of successes and failures
	score.RecordSuccess(50.0)
	score.RecordSuccess(100.0)
	score.RecordFailure()
	score.RecordFailure()
	score.RecordFailure()

	// Record some forwards
	score.RecordMessageAccepted()
	score.RecordMessageAccepted()
	score.RecordMessageForwarded()

	// Score should be lower than baseline due to failures (40% success ratio)
	// With 40% success ratio, expect score around 0.4-0.7 range
	if score.Score > 0.8 {
		t.Errorf("expected lower score with failures, got %f", score.Score)
	}

	// But should still be positive
	if score.Score < 0 {
		t.Errorf("expected positive score, got %f", score.Score)
	}
}

func TestRelayScoreClamping(t *testing.T) {
	score := NewRelayScore("test-relay")

	// Apply extreme penalties to drive score below minimum
	for i := 0; i < 100; i++ {
		score.RecordFailure()
		score.RecordDisconnect()
		score.RecordHandshakeFailure()
	}

	// Score should be clamped to MIN_SCORE
	if score.Score < MIN_SCORE {
		t.Errorf("score should be clamped to minimum %f, got %f", MIN_SCORE, score.Score)
	}

	// Now test upper bound
	score2 := NewRelayScore("test-relay-2")
	for i := 0; i < 100; i++ {
		score2.RecordSuccess(10.0)
	}

	// Score should be clamped to MAX_SCORE
	if score2.Score > MAX_SCORE {
		t.Errorf("score should be clamped to maximum %f, got %f", MAX_SCORE, score2.Score)
	}
}
