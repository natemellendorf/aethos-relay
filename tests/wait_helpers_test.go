package tests

import (
	"testing"
	"time"
)

const waitPollInterval = 20 * time.Millisecond

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()

	if condition() {
		return
	}

	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-timeoutTimer.C:
			t.Fatalf("condition did not become true before timeout: %s", description)
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

func waitForConditionToStayFalse(t *testing.T, duration time.Duration, condition func() bool, description string) {
	t.Helper()

	if condition() {
		t.Fatalf("condition unexpectedly true at start of wait: %s", description)
	}

	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()

	timer := time.NewTimer(duration)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return
		case <-ticker.C:
			if condition() {
				t.Fatalf("condition became true before wait window elapsed: %s", description)
			}
		}
	}
}
