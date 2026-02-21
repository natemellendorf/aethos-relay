package api

import (
	"net/http"
	"testing"
)

func TestOriginChecker_DevMode(t *testing.T) {
	// Dev mode should allow all origins
	oc := NewOriginChecker("", true)

	req, _ := http.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "http://malicious-site.com")

	if !oc.Check(req) {
		t.Error("dev mode should allow all origins")
	}
}

func TestOriginChecker_AllowedOrigins(t *testing.T) {
	// Production mode with allowed origins
	oc := NewOriginChecker("https://app.aethos.io,https://aethos.app", false)

	tests := []struct {
		origin      string
		shouldAllow bool
	}{
		{"https://app.aethos.io", true},
		{"https://aethos.app", true},
		{"https://malicious-site.com", false},
		{"http://localhost:3000", false},
		{"", true}, // Same-origin request without origin header
	}

	for _, tc := range tests {
		req, _ := http.NewRequest("GET", "/ws", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}

		result := oc.Check(req)
		if result != tc.shouldAllow {
			t.Errorf("origin %q: expected allow=%v, got %v", tc.origin, tc.shouldAllow, result)
		}
	}
}

func TestOriginChecker_NoOriginsConfigured(t *testing.T) {
	// Production mode with no allowed origins - should deny all
	oc := NewOriginChecker("", false)

	req, _ := http.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "https://app.aethos.io")

	if oc.Check(req) {
		t.Error("should deny when no origins configured")
	}
}

func TestOriginChecker_EmptyOrigin(t *testing.T) {
	// Request without origin header (same-origin)
	oc := NewOriginChecker("https://app.aethos.io", false)

	req, _ := http.NewRequest("GET", "/ws", nil)
	// No Origin header set

	if !oc.Check(req) {
		t.Error("should allow requests without origin header (same-origin)")
	}
}
