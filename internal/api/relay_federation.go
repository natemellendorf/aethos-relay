package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/gossip"
	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

var relayUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // For relay-to-relay; in production, check relay list
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// RelayFederationHandler handles relay-to-relay federation.
type RelayFederationHandler struct {
	store  store.DescriptorStore
	gossip *gossip.Gossip
	peers  *model.ClientRegistry
	maxTTL time.Duration
	ticker *time.Ticker
	stopCh chan struct{}
}

// NewRelayFederationHandler creates a new relay federation handler.
func NewRelayFederationHandler(store store.DescriptorStore, peers *model.ClientRegistry, maxTTL time.Duration) *RelayFederationHandler {
	h := &RelayFederationHandler{
		store:  store,
		gossip: gossip.NewGossip(store),
		peers:  peers,
		maxTTL: maxTTL,
		ticker: time.NewTicker(5 * time.Minute), // Gossip every 5 minutes
		stopCh: make(chan struct{}),
	}
	return h
}

// Start begins the gossip tick loop.
func (h *RelayFederationHandler) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-h.ticker.C:
				h.gossipTick(ctx)
			case <-h.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop stops the gossip handler.
func (h *RelayFederationHandler) Stop() {
	h.ticker.Stop()
	close(h.stopCh)
}

// gossipTick performs periodic gossip to connected relay peers.
func (h *RelayFederationHandler) gossipTick(ctx context.Context) {
	// Select sample of descriptors to share
	sample, err := h.gossip.SelectSample(ctx)
	if err != nil {
		log.Printf("federation: failed to select gossip sample: %v", err)
		return
	}

	if len(sample) == 0 {
		return
	}

	// Broadcast to connected relay peers
	// In a full implementation, this would identify relay peers vs client connections
	// For now, we broadcast to all connections (they'll filter if not relay peers)
	frame := model.WSFrame{
		Type:             model.FrameTypeRelayDescriptors,
		RelayDescriptors: sample,
	}

	_, err = json.Marshal(frame)
	if err != nil {
		log.Printf("federation: failed to marshal gossip: %v", err)
		return
	}

	// This would need proper peer filtering in production
	log.Printf("federation: prepared gossip with %d descriptors", len(sample))
}

// HandleRelayWebSocket handles relay-to-relay WebSocket connections.
func (h *RelayFederationHandler) HandleRelayWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := relayUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("federation: failed to upgrade: %v", err)
		return
	}
	defer conn.Close()

	metrics.ConnectionsCurrent.Inc()
	defer metrics.ConnectionsCurrent.Dec()

	// Capture remote address before starting goroutine
	remoteAddr := r.RemoteAddr

	client := &model.Client{
		Conn:       conn,
		Send:       make(chan []byte, 256),
		WayfarerID: "", // Will be set on hello
	}

	// Start writer goroutine
	go h.writePump(client)

	// Start reader goroutine
	h.readPump(client, remoteAddr)
}

// readPump reads messages from the relay WebSocket.
func (h *RelayFederationHandler) readPump(client *model.Client, peerAddr string) {
	defer client.Conn.Close()

	for {
		var frame model.WSFrame
		err := client.Conn.ReadJSON(&frame)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("federation: read error from %s: %v", peerAddr, err)
			}
			break
		}

		h.handleRelayFrame(client, &frame, peerAddr)
	}
}

// writePump writes messages to the relay WebSocket.
func (h *RelayFederationHandler) writePump(client *model.Client) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		client.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.Send:
			if !ok {
				client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleRelayFrame handles relay-specific frames.
func (h *RelayFederationHandler) handleRelayFrame(client *model.Client, frame *model.WSFrame, peerAddr string) {
	switch frame.Type {
	case model.FrameTypeHello:
		h.handleHello(client, frame, peerAddr)
	case model.FrameTypeRelayDescriptors:
		h.handleRelayDescriptors(client, frame, peerAddr)
	default:
		// Unknown frame type - ignore or log
		log.Printf("federation: unknown frame type: %s", frame.Type)
	}
}

// handleHello handles the hello frame from a relay peer.
func (h *RelayFederationHandler) handleHello(client *model.Client, frame *model.WSFrame, peerAddr string) {
	if frame.WayfarerID == "" {
		h.sendError(client, "wayfarer_id required")
		return
	}

	// Register as relay peer
	client.WayfarerID = frame.WayfarerID
	h.peers.Register(client)

	log.Printf("federation: relay %s connected from %s", frame.WayfarerID, peerAddr)

	// Send hello_ok
	h.send(client, model.WSFrame{Type: model.FrameTypeHelloOK})

	// Trigger immediate gossip share
	go h.shareGossip(client)
}

// handleRelayDescriptors handles incoming relay descriptors.
func (h *RelayFederationHandler) handleRelayDescriptors(client *model.Client, frame *model.WSFrame, peerAddr string) {
	if client.WayfarerID == "" {
		h.sendError(client, "not authenticated as relay")
		return
	}

	descriptors := frame.RelayDescriptors
	if len(descriptors) == 0 {
		return
	}

	// Process descriptors with rate limiting
	peerID := client.WayfarerID
	acks, err := h.gossip.HandleDescriptors(context.Background(), peerID, descriptors)
	if err != nil {
		log.Printf("federation: error handling descriptors from %s: %v", peerAddr, err)
		h.sendError(client, "internal error")
		return
	}

	// Send acknowledgment
	h.send(client, model.WSFrame{
		Type:                model.FrameTypeRelayDescriptorAck,
		RelayDescriptorAcks: acks,
	})
}

// shareGossip sends current descriptors to a newly connected peer.
func (h *RelayFederationHandler) shareGossip(client *model.Client) {
	sample, err := h.gossip.SelectSample(context.Background())
	if err != nil {
		log.Printf("federation: failed to select sample for peer: %v", err)
		return
	}

	if len(sample) == 0 {
		return
	}

	h.send(client, model.WSFrame{
		Type:             model.FrameTypeRelayDescriptors,
		RelayDescriptors: sample,
	})
}

// send sends a frame to the relay.
func (h *RelayFederationHandler) send(client *model.Client, frame model.WSFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("federation: failed to marshal frame: %v", err)
		return
	}
	select {
	case client.Send <- data:
	case <-time.After(1 * time.Second):
		log.Printf("federation: failed to send, channel full")
	}
}

// sendError sends an error frame.
func (h *RelayFederationHandler) sendError(client *model.Client, err string) {
	h.send(client, model.WSFrame{
		Type:  model.FrameTypeError,
		MsgID: err,
	})
}
