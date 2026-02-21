package federation

import (
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	// Create rate limiter: 5 requests per window
	limiter := NewRateLimiter(5, time.Minute)

	peerID := "test-peer-1"

	// Should allow first 5 requests
	for i := 0; i < 5; i++ {
		if !limiter.Allow(peerID) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 6th request should be denied
	if limiter.Allow(peerID) {
		t.Error("6th request should be denied")
	}

	// Different peer should be allowed
	if !limiter.Allow("test-peer-2") {
		t.Error("different peer should be allowed")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	// Create rate limiter with short window for testing
	limiter := NewRateLimiter(5, 50*time.Millisecond)

	peerID := "test-peer"

	// Use up the limit
	for i := 0; i < 5; i++ {
		limiter.Allow(peerID)
	}

	// Should be denied
	if limiter.Allow(peerID) {
		t.Error("should be rate limited")
	}

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Run cleanup
	limiter.Cleanup()

	// Should now be allowed again
	if !limiter.Allow(peerID) {
		t.Error("should be allowed after window expires and cleanup")
	}
}

func TestPeerHealth_RecordFailure(t *testing.T) {
	health := &PeerHealth{}

	// Record failures
	health.RecordFailure()
	if health.FailureCount != 1 {
		t.Errorf("expected failure count 1, got %d", health.FailureCount)
	}
	if health.IsHealthy {
		t.Error("expected unhealthy after failure")
	}

	health.RecordFailure()
	if health.FailureCount != 2 {
		t.Errorf("expected failure count 2, got %d", health.FailureCount)
	}
}

func TestPeerHealth_RecordSuccess(t *testing.T) {
	health := &PeerHealth{FailureCount: 3, IsHealthy: false}

	health.RecordSuccess()

	if health.FailureCount != 0 {
		t.Errorf("expected failure count 0, got %d", health.FailureCount)
	}
	if !health.IsHealthy {
		t.Error("expected healthy after success")
	}
}

func TestPeerHealth_IsBackingOff(t *testing.T) {
	health := &PeerHealth{}

	// Not backing off initially
	if health.IsBackingOff() {
		t.Error("should not be backing off initially")
	}

	// Set backoff
	health.BackoffUntil = time.Now().Add(time.Minute)

	// Should be backing off
	if !health.IsBackingOff() {
		t.Error("should be backing off")
	}

	// Wait for backoff to expire
	time.Sleep(10 * time.Millisecond)
	health.BackoffUntil = time.Now().Add(-time.Minute)

	// Should not be backing off
	if health.IsBackingOff() {
		t.Error("should not be backing off after backoff period")
	}
}
