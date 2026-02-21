package api

import (
	"net/http"

	"github.com/natemellendorf/aethos-relay/internal/federation"
)

// FederationPeersHandler handles /federation/peers endpoint.
type FederationPeersHandler struct {
	peerManager *federation.PeerManager
}

// NewFederationPeersHandler creates a new federation peers handler.
func NewFederationPeersHandler(peerManager *federation.PeerManager) *FederationPeersHandler {
	return &FederationPeersHandler{
		peerManager: peerManager,
	}
}

// ServeHTTP handles federation peers requests.
func (h *FederationPeersHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetPeers(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *FederationPeersHandler) handleGetPeers(w http.ResponseWriter, r *http.Request) {
	peers := h.peerManager.GetPeerMetrics()

	JSON(w, http.StatusOK, map[string]interface{}{
		"peers": peers,
		"count": len(peers),
	})
}
