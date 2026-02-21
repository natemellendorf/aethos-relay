package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

// RelayFederationHandler handles relay-to-relay descriptor exchange.
type RelayFederationHandler struct {
	descriptorStore store.DescriptorStore
	relayID         string
	enabled         bool
}

// NewRelayFederationHandler creates a new relay federation handler.
func NewRelayFederationHandler(descriptorStore store.DescriptorStore, relayID string, enabled bool) *RelayFederationHandler {
	return &RelayFederationHandler{
		descriptorStore: descriptorStore,
		relayID:         relayID,
		enabled:         enabled,
	}
}

// HandleRelayDescriptors handles incoming relay_descriptors frame.
func (h *RelayFederationHandler) HandleRelayDescriptors(peerID string, frame *model.RelayDescriptorFrame) error {
	if !h.enabled {
		return nil // Silently ignore if not enabled
	}

	metrics.IncrementDescriptorsReceived()

	// Rate limit check
	peerCount, err := h.descriptorStore.GetPeerDescriptorCount(context.Background(), peerID)
	if err != nil {
		log.Printf("relay_federation: failed to get peer descriptor count: %v", err)
	}
	if peerCount >= model.MAX_DESCRIPTORS_PER_PEER_PER_HOUR {
		metrics.IncrementDescriptorsRejectedRateLimit()
		log.Printf("relay_federation: rate limited peer %s (%d descriptors this hour)", peerID, peerCount)
		return model.ErrDescriptorRateLimited
	}

	accepted := 0
	rejected := 0

	for _, desc := range frame.Descriptors {
		// Validate descriptor
		if err := desc.Validate(); err != nil {
			metrics.IncrementDescriptorsRejectedValidation()
			rejected++
			log.Printf("relay_federation: descriptor validation failed for %s: %v", desc.RelayID, err)
			continue
		}

		// Check registry capacity
		count, err := h.descriptorStore.CountDescriptors(context.Background())
		if err != nil {
			log.Printf("relay_federation: failed to count descriptors: %v", err)
			continue
		}
		if count >= model.MAX_TOTAL_DESCRIPTORS {
			metrics.IncrementDescriptorsRejectedRateLimit()
			rejected++
			log.Printf("relay_federation: registry full, rejecting descriptor %s", desc.RelayID)
			continue
		}

		// Check if this is an update (dedupe)
		existing, err := h.descriptorStore.GetDescriptor(context.Background(), desc.RelayID)
		if err != nil {
			log.Printf("relay_federation: failed to get descriptor: %v", err)
			continue
		}

		if existing != nil {
			// Update: dedupe by updating last_seen_at
			desc.FirstSeenAt = existing.FirstSeenAt
		}

		// Set advertised_by if not set
		if desc.AdvertisedBy == "" {
			desc.AdvertisedBy = peerID
		}

		// Store descriptor
		if err := h.descriptorStore.PutDescriptor(context.Background(), &desc); err != nil {
			log.Printf("relay_federation: failed to store descriptor %s: %v", desc.RelayID, err)
			rejected++
			continue
		}

		accepted++
	}

	// Update metrics
	if accepted > 0 {
		metrics.IncrementDescriptorsAccepted()
	}
	if rejected > 0 {
		metrics.IncrementDescriptorsRejectedValidation()
	}

	// Update registry size metric
	total, _ := h.descriptorStore.CountDescriptors(context.Background())
	metrics.SetDescriptorsRegistrySize(total)

	log.Printf("relay_federation: processed %d descriptors (accepted: %d, rejected: %d)", len(frame.Descriptors), accepted, rejected)

	return nil
}

// HandleRelayDescriptorAck handles optional acknowledgment frame.
func (h *RelayFederationHandler) HandleRelayDescriptorAck(peerID string, frame *model.RelayDescriptorAckFrame) error {
	// Currently, we don't need to do anything with acknowledgments
	// This is here for future extensibility
	log.Printf("relay_federation: received ack from %s for %d relay IDs", peerID, len(frame.RelayIDs))
	return nil
}

// GetDescriptorsForGossip returns descriptors to share with peers.
func (h *RelayFederationHandler) GetDescriptorsForGossip(limit int) ([]model.RelayDescriptor, error) {
	descriptors, err := h.descriptorStore.GetAllDescriptors(context.Background())
	if err != nil {
		return nil, err
	}

	// Sort by freshness and limit
	// Freshest descriptors first (most recently seen)
	type scoredDesc struct {
		desc  *model.RelayDescriptor
		score time.Time
	}

	scored := make([]scoredDesc, len(descriptors))
	for i, d := range descriptors {
		scored[i] = scoredDesc{desc: d, score: d.LastSeenAt}
	}

	// Sort by score descending (freshest first)
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score.After(scored[i].score) {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	result := make([]model.RelayDescriptor, 0, min(len(scored), limit))
	for i := 0; i < len(scored) && i < limit; i++ {
		result = append(result, *scored[i].desc)
	}

	return result, nil
}

// RelayDescriptorHandler handles relay descriptor HTTP endpoint.
type RelayDescriptorHandler struct {
	store   store.DescriptorStore
	enabled bool
}

// NewRelayDescriptorHandler creates a new relay descriptor handler.
func NewRelayDescriptorHandler(store store.DescriptorStore, enabled bool) *RelayDescriptorHandler {
	return &RelayDescriptorHandler{
		store:   store,
		enabled: enabled,
	}
}

// ServeHTTP handles descriptor requests.
func (h *RelayDescriptorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Error(w, "relay discovery disabled", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *RelayDescriptorHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	descriptors, err := h.store.GetAllDescriptors(ctx)
	if err != nil {
		http.Error(w, "failed to get descriptors", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"descriptors": descriptors,
		"count":       len(descriptors),
	})
}

func (h *RelayDescriptorHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	var frame model.RelayDescriptorFrame
	if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Process descriptors (use "local" as peerID since this is self-advertisement)
	handler := NewRelayFederationHandler(h.store, "local", h.enabled)
	if err := handler.HandleRelayDescriptors("local", &frame); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
