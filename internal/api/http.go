package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTPHandler handles HTTP requests.
type HTTPHandler struct {
	store      store.Store
	sweeper    *store.TTLSweeper
	maxTTLSecs int
}

// NewHTTPHandler creates a new HTTP handler.
func NewHTTPHandler(store store.Store, sweeper *store.TTLSweeper, maxTTLSecs int) *HTTPHandler {
	return &HTTPHandler{
		store:      store,
		sweeper:    sweeper,
		maxTTLSecs: maxTTLSecs,
	}
}

// ServeHTTP serves HTTP requests.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		h.handleHealthz(w, r)
	case "/metrics":
		promhttp.Handler().ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleHealthz handles health check requests.
func (h *HTTPHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
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
			// Allow up to 2x interval before marking unhealthy
			threshold := time.Duration(h.maxTTLSecs) * 2 * time.Second
			if time.Since(lastSweep) > threshold {
				http.Error(w, "sweeper stalled", http.StatusServiceUnavailable)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
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
