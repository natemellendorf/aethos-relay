package api

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTPHandler handles HTTP requests.
type HTTPHandler struct {
	store      store.Store
	sweeper    *store.TTLSweeper
	maxTTLSecs int
	wsActive   atomic.Int32
	storeInit  atomic.Bool
}

// NewHTTPHandler creates a new HTTP handler.
func NewHTTPHandler(store store.Store, sweeper *store.TTLSweeper, maxTTLSecs int) *HTTPHandler {
	h := &HTTPHandler{
		store:      store,
		sweeper:    sweeper,
		maxTTLSecs: maxTTLSecs,
	}
	// Note: storeInit defaults to false (atomic.Bool zero value)
	// Caller must call SetStoreInitialized() when ready
	return h
}

// SetStoreInitialized marks the store as initialized.
func (h *HTTPHandler) SetStoreInitialized() {
	h.storeInit.Store(true)
}

// IncrementWSActive increments the active WebSocket counter.
func (h *HTTPHandler) IncrementWSActive() {
	h.wsActive.Add(1)
}

// DecrementWSActive decrements the active WebSocket counter.
func (h *HTTPHandler) DecrementWSActive() {
	h.wsActive.Add(-1)
}

// WSActiveCount returns the current number of active WebSocket connections.
func (h *HTTPHandler) WSActiveCount() int32 {
	return h.wsActive.Load()
}

// ServeHTTP serves HTTP requests.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		h.handleHealthz(w, r)
	case "/readyz":
		h.handleReadyz(w, r)
	case "/metrics":
		promhttp.Handler().ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleHealthz handles health check requests.
// Returns 200 if the process is up and store is open.
func (h *HTTPHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// Check store health - try a simple read operation
	_, err := h.store.GetLastSweepTime(r.Context())
	if err != nil {
		http.Error(w, "store unhealthy", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleReadyz handles readiness check requests.
// Returns 200 if the store is initialized and WS listeners are active.
func (h *HTTPHandler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Check store is initialized
	if !h.storeInit.Load() {
		http.Error(w, "store not initialized", http.StatusServiceUnavailable)
		return
	}

	// Check store health - try a simple read operation
	_, err := h.store.GetLastSweepTime(r.Context())
	if err != nil {
		http.Error(w, "store unhealthy", http.StatusServiceUnavailable)
		return
	}

	// Check sweeper health - ensure it ran recently
	if h.sweeper != nil {
		lastSweep, err := h.sweeper.GetLastSweepTime(r.Context())
		if err == nil && !lastSweep.IsZero() {
			// Allow up to 2x interval before marking not ready
			threshold := time.Duration(h.maxTTLSecs) * 2 * time.Second
			if time.Since(lastSweep) > threshold {
				http.Error(w, "sweeper stalled", http.StatusServiceUnavailable)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}

// JSON writes a JSON response.
func JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

// JSONError writes a JSON error response.
func JSONError(w http.ResponseWriter, status int, err string) {
	JSON(w, status, map[string]string{"error": err})
}
