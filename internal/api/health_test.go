package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
)

// mockStore implements a minimal store for testing
type mockStore struct {
	store.Store
	lastSweepTime time.Time
}

func (m *mockStore) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return m.lastSweepTime, nil
}

func (m *mockStore) SetLastSweepTime(ctx context.Context, t time.Time) error {
	m.lastSweepTime = t
	return nil
}

// mockSweeper implements a minimal sweeper for testing
type mockSweeper struct {
	lastSweepTime time.Time
}

func (m *mockSweeper) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return m.lastSweepTime, nil
}

func TestHTTPHandler_Healthz(t *testing.T) {
	// Create handler with mock store
	ms := &mockStore{lastSweepTime: time.Now()}
	sweeper := &mockSweeper{lastSweepTime: time.Now()}

	// Need to cast to get sweeper interface
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)
	_ = sweeper // just to avoid unused warning

	handler := NewHTTPHandler(ms, realSweeper, 86400)

	// Test /healthz endpoint
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if w.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got '%s'", w.Body.String())
	}
}

func TestHTTPHandler_Readyz(t *testing.T) {
	// Create handler with mock store
	ms := &mockStore{lastSweepTime: time.Now()}
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(ms, realSweeper, 86400)
	handler.SetStoreInitialized()

	// Test /readyz endpoint
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if w.Body.String() != "ready" {
		t.Errorf("expected body 'ready', got '%s'", w.Body.String())
	}
}

func TestHTTPHandler_Readyz_NotInitialized(t *testing.T) {
	// Create handler with mock store
	ms := &mockStore{lastSweepTime: time.Now()}
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(ms, realSweeper, 86400)
	// Don't call SetStoreInitialized - store is not ready

	// Test /readyz endpoint - should fail because not initialized
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestHTTPHandler_Healthz_StoreError(t *testing.T) {
	// Create handler with failing store
	failingStore := &failingStore{}
	realSweeper := store.NewTTLSweeper(failingStore, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(failingStore, realSweeper, 86400)

	// Test /healthz endpoint - should fail
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestHTTPHandler_Metrics(t *testing.T) {
	// Create handler
	ms := &mockStore{lastSweepTime: time.Now()}
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(ms, realSweeper, 86400)

	// Test /metrics endpoint
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Prometheus metrics should return some content
	if w.Body.Len() == 0 {
		t.Error("expected metrics content")
	}
}

func TestHTTPHandler_NotFound(t *testing.T) {
	// Create handler
	ms := &mockStore{lastSweepTime: time.Now()}
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(ms, realSweeper, 86400)

	// Test unknown endpoint
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHTTPHandler_WSActiveCount(t *testing.T) {
	ms := &mockStore{lastSweepTime: time.Now()}
	realSweeper := store.NewTTLSweeper(ms, 30*time.Second, 24*time.Hour)

	handler := NewHTTPHandler(ms, realSweeper, 86400)

	// Initial count should be 0
	if handler.WSActiveCount() != 0 {
		t.Errorf("expected initial count 0, got %d", handler.WSActiveCount())
	}

	// Increment
	handler.IncrementWSActive()
	if handler.WSActiveCount() != 1 {
		t.Errorf("expected count 1, got %d", handler.WSActiveCount())
	}

	// Decrement
	handler.DecrementWSActive()
	if handler.WSActiveCount() != 0 {
		t.Errorf("expected count 0, got %d", handler.WSActiveCount())
	}
}

// failingStore always returns errors
type failingStore struct {
	store.Store
}

func (f *failingStore) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return time.Time{}, &mockError{"store error"}
}

func (f *failingStore) SetLastSweepTime(ctx context.Context, t time.Time) error {
	return &mockError{"store error"}
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()
	type TestResponse struct {
		Status string `json:"status"`
	}

	JSON(w, http.StatusOK, TestResponse{Status: "ok"})

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp TestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got '%s'", resp.Status)
	}
}

func TestJSONError(t *testing.T) {
	w := httptest.NewRecorder()

	JSONError(w, http.StatusBadRequest, "invalid request")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["error"] != "invalid request" {
		t.Errorf("expected error 'invalid request', got '%s'", resp["error"])
	}
}
