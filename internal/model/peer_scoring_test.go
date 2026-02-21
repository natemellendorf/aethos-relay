package model

import (
	"testing"
	"time"
)

func TestPeerMetricsScoreSensitivity(t *testing.T) {
	// Test 1: Better success rate should yield higher score
	metrics1 := NewPeerMetrics("peer1")
	// Record successful acks
	for i := 0; i < 10; i++ {
		metrics1.RecordAck(100.0)
	}

	metrics2 := NewPeerMetrics("peer2")
	// Record mix of success and failure
	for i := 0; i < 5; i++ {
		metrics2.RecordAck(100.0)
	}
	metrics2.RecordFailure()
	metrics2.RecordFailure()

	if metrics1.Score <= metrics2.Score {
		t.Errorf("Expected peer1 (better success rate) to have higher score than peer2, got peer1=%.2f, peer2=%.2f", metrics1.Score, metrics2.Score)
	}

	// Test 2: Failures should lower score
	metrics3 := NewPeerMetrics("peer3")
	// Record many failures
	for i := 0; i < 10; i++ {
		metrics3.RecordFailure()
	}

	if metrics3.Score >= metrics1.Score {
		t.Errorf("Expected peer3 (failures) to have lower score than peer1 (success), got peer3=%.2f, peer1=%.2f", metrics3.Score, metrics1.Score)
	}

	// Test 3: Consecutive failures should lower score more
	metrics4 := NewPeerMetrics("peer4")
	metrics4.RecordFailure()
	metrics4.RecordFailure()
	metrics4.RecordFailure()

	metrics5 := NewPeerMetrics("peer5")
	// Non-consecutive failures
	metrics5.RecordFailure()
	metrics5.RecordAck(100.0)
	metrics5.RecordFailure()
	metrics5.RecordAck(100.0)
	metrics5.RecordFailure()

	if metrics4.Score >= metrics5.Score {
		t.Errorf("Expected peer4 (consecutive failures) to have lower score than peer5 (non-consecutive), got peer4=%.2f, peer5=%.2f", metrics4.Score, metrics5.Score)
	}
}

func TestPeerMetricsBackoff(t *testing.T) {
	metrics := NewPeerMetrics("peer1")

	// Record multiple failures to trigger backoff
	metrics.RecordFailure()
	metrics.ApplyBackoff(1*time.Second, 60*time.Second)

	if !metrics.IsBackingOff() {
		t.Error("Expected peer to be backing off after failure")
	}

	// Clear backoff after success
	metrics.RecordAck(100.0)
	metrics.ClearBackoff()

	if metrics.IsBackingOff() {
		t.Error("Expected peer to not be backing off after success")
	}
}

func TestPeerMetricsDecay(t *testing.T) {
	metrics := NewPeerMetrics("peer1")

	// Record successful acks
	for i := 0; i < 10; i++ {
		metrics.RecordAck(100.0)
	}

	originalScore := metrics.Score

	// Simulate time passing without acks
	metrics.LastAckAt = time.Now().Add(-10 * time.Minute)
	metrics.ApplyDecay()

	if metrics.Score >= originalScore {
		t.Errorf("Expected score to decay after time without acks, got before=%.2f, after=%.2f", originalScore, metrics.Score)
	}
}

func TestPeerMetricsIsHealthy(t *testing.T) {
	// Test healthy peer
	healthy := NewPeerMetrics("healthy")
	healthy.RecordAck(100.0)

	if !healthy.IsHealthy() {
		t.Error("Expected peer with good score to be healthy")
	}

	// Test unhealthy peer (low score)
	unhealthy := NewPeerMetrics("unhealthy")
	for i := 0; i < 20; i++ {
		unhealthy.RecordFailure()
	}

	if unhealthy.IsHealthy() {
		t.Error("Expected peer with many failures to not be healthy")
	}

	// Test disconnected peer
	disconnected := NewPeerMetrics("disconnected")
	disconnected.RecordDisconnect()

	if disconnected.IsHealthy() {
		t.Error("Expected disconnected peer to not be healthy")
	}
}
