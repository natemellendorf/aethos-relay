package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

// OriginChecker validates WebSocket origin against an allowlist.
type OriginChecker struct {
	allowedOrigins map[string]bool
	devMode        bool
}

// NewOriginChecker creates a new origin checker.
// allowedOrigins is a comma-separated list of allowed origins (e.g., "https://app.aethos.io,https://aethos.app")
// devMode when true allows all origins (for local development)
func NewOriginChecker(allowedOrigins string, devMode bool) *OriginChecker {
	oc := &OriginChecker{
		allowedOrigins: make(map[string]bool),
		devMode:        devMode,
	}
	// In dev mode, allow all origins
	if devMode {
		return oc
	}
	// Parse allowed origins
	for _, origin := range strings.Split(allowedOrigins, ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			oc.allowedOrigins[origin] = true
		}
	}
	return oc
}

// Check validates if the origin is allowed.
func (oc *OriginChecker) Check(r *http.Request) bool {
	// Dev mode allows all origins
	if oc.devMode {
		return true
	}
	// If no origins configured, deny all
	if len(oc.allowedOrigins) == 0 {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header - check if it's a same-origin request
		return true
	}
	return oc.allowedOrigins[origin]
}

// WSHandler handles WebSocket connections.
type WSHandler struct {
	store             store.Store
	clients           *model.ClientRegistry
	maxTTL            time.Duration
	originChecker     *OriginChecker
	federationManager *federation.PeerManager
	autoDeliverQueued bool
}

// NewWSHandler creates a new WebSocket handler.
// allowedOrigins is a comma-separated list of allowed origins.
// devMode enables relaxed origin checking for local development.
func NewWSHandler(store store.Store, clients *model.ClientRegistry, maxTTL time.Duration, allowedOrigins string, devMode bool) *WSHandler {
	originChecker := NewOriginChecker(allowedOrigins, devMode)
	return &WSHandler{
		store:             store,
		clients:           clients,
		maxTTL:            maxTTL,
		originChecker:     originChecker,
		autoDeliverQueued: true,
	}
}

// SetFederationManager sets the federation peer manager for relaying messages.
func (h *WSHandler) SetFederationManager(mgr *federation.PeerManager) {
	h.federationManager = mgr
}

// SetAutoDeliverQueued controls automatic queued delivery on hello.
// This defaults to true and exists to enable deterministic integration tests.
func (h *WSHandler) SetAutoDeliverQueued(enabled bool) {
	h.autoDeliverQueued = enabled
}

// HandleWebSocket upgrades the connection and handles WebSocket messaging.
func (h *WSHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check origin before upgrading
	if !h.originChecker.Check(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Origin already validated above
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: failed to upgrade: %v", err)
		return
	}
	defer conn.Close()

	metrics.ConnectionsCurrent.Inc()
	defer metrics.ConnectionsCurrent.Dec()

	client := &model.Client{
		Conn:       conn,
		Send:       make(chan []byte, 256),
		WayfarerID: "",
	}

	// Start writer goroutine
	go h.writePump(client)

	// Start reader goroutine
	h.readPump(client)
}

// readPump reads messages from the WebSocket.
func (h *WSHandler) readPump(client *model.Client) {
	defer func() {
		if client.WayfarerID != "" {
			h.clients.Unregister(client)
		}
		client.Conn.Close()
	}()

	for {
		var frame model.WSFrame
		err := client.Conn.ReadJSON(&frame)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws: read error: %v", err)
			}
			break
		}

		h.handleFrame(client, &frame)
	}
}

// writePump writes messages to the WebSocket.
func (h *WSHandler) writePump(client *model.Client) {
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

// handleFrame handles incoming WebSocket frames.
func (h *WSHandler) handleFrame(client *model.Client, frame *model.WSFrame) {
	metrics.IncrementReceived()

	// Reject relay-only frame types on client connections.
	if model.IsRelayFrameType(frame.Type) {
		h.sendError(client, "relay frame type not allowed on client connections")
		return
	}

	switch frame.Type {
	case model.FrameTypeHello:
		h.handleHello(client, frame)
	case model.FrameTypeSend:
		h.handleSend(client, frame)
	case model.FrameTypeAck:
		h.handleAck(client, frame)
	case model.FrameTypePull:
		h.handlePull(client, frame)
	default:
		h.sendError(client, "unknown frame type")
	}
}

// handleHello handles the hello frame.
func (h *WSHandler) handleHello(client *model.Client, frame *model.WSFrame) {
	if frame.WayfarerID == "" {
		h.sendError(client, "wayfarer_id required")
		return
	}

	// Register client
	client.WayfarerID = frame.WayfarerID
	client.ID = uuid.New().String()
	h.clients.Register(client)

	// Send hello_ok
	h.send(client, model.WSFrame{Type: model.FrameTypeHelloOK})

	// Deliver any queued messages
	if h.autoDeliverQueued {
		go h.deliverQueuedMessages(client)
	}
}

// handleSend handles the send frame.
func (h *WSHandler) handleSend(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, "not authenticated")
		return
	}
	if frame.To == "" {
		h.sendError(client, "recipient required")
		return
	}
	if frame.PayloadB64 == "" {
		h.sendError(client, "payload required")
		return
	}

	// Validate TTL
	ttl := time.Duration(frame.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = h.maxTTL
	}
	if ttl > h.maxTTL {
		ttl = h.maxTTL
	}

	now := time.Now()
	msg := &model.Message{
		ID:        uuid.New().String(),
		From:      client.WayfarerID,
		To:        frame.To,
		Payload:   frame.PayloadB64,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Delivered: false,
	}

	// Check if recipient is online
	online := h.clients.IsOnline(frame.To)
	log.Printf("ws: send msg_id=%s from=%s to=%s ttl=%ds online=%t", msg.ID, msg.From, msg.To, int(ttl.Seconds()), online)

	if online {
		// Deliver immediately - but still persist for durability
		if err := h.store.PersistMessage(context.Background(), msg); err != nil {
			metrics.IncrementStoreErrors()
			h.sendError(client, "failed to persist message")
			return
		}
		metrics.IncrementPersisted()

		// Try to deliver
		h.deliverToRecipient(msg)
	} else {
		// Persist for later delivery
		if err := h.store.PersistMessage(context.Background(), msg); err != nil {
			metrics.IncrementStoreErrors()
			h.sendError(client, "failed to persist message")
			return
		}
		metrics.IncrementPersisted()
	}

	// Send send_ok
	h.send(client, model.WSFrame{
		Type:  model.FrameTypeSendOK,
		MsgID: msg.ID,
		At:    msg.CreatedAt.Unix(),
	})

	// Announce to federation peers (if federation is enabled)
	if h.federationManager != nil {
		h.federationManager.AnnounceMessage(msg)
	}
}

// handleAck handles the ack frame.
func (h *WSHandler) handleAck(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, "not authenticated")
		return
	}
	if frame.MsgID == "" {
		h.sendError(client, "msg_id required")
		return
	}

	// Mark as delivered to this specific recipient in store
	if err := h.store.MarkDelivered(context.Background(), frame.MsgID, client.WayfarerID); err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, "failed to acknowledge message")
		return
	}
	metrics.IncrementDelivered()

	log.Printf("ws: ack msg_id=%s from=%s to=%s ttl=%ds", frame.MsgID, client.WayfarerID, client.WayfarerID, 0)

	// Send ack_ok
	h.send(client, model.WSFrame{
		Type:  model.FrameTypeAckOK,
		MsgID: frame.MsgID,
	})
}

// handlePull handles the pull frame.
func (h *WSHandler) handlePull(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, "not authenticated")
		return
	}

	limit := frame.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	messages, err := h.store.GetQueuedMessages(context.Background(), client.WayfarerID, limit)
	if err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, "failed to pull messages")
		return
	}

	// Convert to response format
	var msgs []model.Message
	for _, m := range messages {
		msgs = append(msgs, model.Message{
			ID:        m.ID,
			From:      m.From,
			To:        m.To,
			Payload:   m.Payload,
			CreatedAt: m.CreatedAt,
			Delivered: m.Delivered,
		})
	}

	h.send(client, model.WSFrame{
		Type:     model.FrameTypeMessages,
		Messages: msgs,
	})
}

// deliverQueuedMessages delivers any queued messages when a client connects.
func (h *WSHandler) deliverQueuedMessages(client *model.Client) {
	messages, err := h.store.GetQueuedMessages(context.Background(), client.WayfarerID, 100)
	if err != nil {
		log.Printf("ws: failed to get queued messages: %v", err)
		return
	}

	for _, msg := range messages {
		h.deliverToRecipient(msg)
	}
}

// deliverToRecipient delivers a message to the recipient if online.
func (h *WSHandler) deliverToRecipient(msg *model.Message) {
	recipients := h.clients.GetClients(msg.To)
	for _, r := range recipients {
		remainingTTL := int(time.Until(msg.ExpiresAt).Seconds())
		if remainingTTL < 0 {
			remainingTTL = 0
		}
		log.Printf("ws: deliver msg_id=%s from=%s to=%s ttl=%ds", msg.ID, msg.From, r.WayfarerID, remainingTTL)
		h.send(r, model.WSFrame{
			Type:       model.FrameTypeMessage,
			MsgID:      msg.ID,
			From:       msg.From,
			PayloadB64: msg.Payload,
			At:         msg.CreatedAt.Unix(),
		})
		// Mark as delivered to this specific recipient
		if err := h.store.MarkDelivered(context.Background(), msg.ID, r.WayfarerID); err != nil {
			metrics.IncrementStoreErrors()
			log.Printf("ws: failed to mark delivered: %v", err)
			continue
		}
		metrics.IncrementDelivered()
	}
}

// send sends a frame to the client with backpressure handling.
// Uses a blocking send with timeout to implement bounded backpressure.
// If the channel is full, it will wait up to 1 second for space.
// This prevents silent message drops while still providing flow control.
func (h *WSHandler) send(client *model.Client, frame model.WSFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("ws: failed to marshal frame: %v", err)
		return
	}
	select {
	case client.Send <- data:
	case <-time.After(1 * time.Second):
		log.Printf("ws: failed to send, channel full after timeout for client %s", client.WayfarerID)
		metrics.IncrementDropped()
	}
}

// sendError sends an error frame to the client.
func (h *WSHandler) sendError(client *model.Client, err string) {
	h.send(client, model.WSFrame{
		Type:  model.FrameTypeError,
		MsgID: err,
	})
}
