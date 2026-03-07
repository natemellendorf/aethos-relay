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
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
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
	engine            *storeforward.Engine
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
		engine:            storeforward.New(store, maxTTL),
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
	conn.SetReadLimit(model.WebSocketReadLimitBytes)
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
		// `ack` marks the message delivered for the tracked recipient identity on this connection.
		h.handleAck(client, frame)
	case model.FrameTypePull:
		h.handlePull(client, frame)
	default:
		h.sendError(client, "unknown frame type")
	}
}

func deliveryIdentityForClient(client *model.Client) string {
	if client.DeliveryID != "" {
		return client.DeliveryID
	}
	return client.WayfarerID
}

// handleHello handles the hello frame.
func (h *WSHandler) handleHello(client *model.Client, frame *model.WSFrame) {
	if frame.WayfarerID == "" {
		h.sendError(client, "wayfarer_id required")
		return
	}

	// Legacy clients omit device_id; in that case, keep the delivery identity at
	// the existing wayfarer-only bucket for backward compatibility.
	deliveryID := storeforward.DeliveryIdentity(frame.WayfarerID, frame.DeviceID)

	// Register client.
	client.WayfarerID = frame.WayfarerID
	client.DeviceID = frame.DeviceID
	client.DeliveryID = deliveryID
	client.ResetDeliveryTracking()
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

	msg, ttl := h.engine.AcceptClientSend(client.WayfarerID, frame.To, frame.PayloadB64, frame.TTLSeconds)

	// Check if recipient is online
	online := h.clients.IsOnline(frame.To)
	log.Printf("ws: send msg_id=%s from=%s to=%s ttl=%ds online=%t", msg.ID, msg.From, msg.To, int(ttl.Seconds()), online)

	if err := h.engine.PersistMessage(context.Background(), msg); err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, "failed to persist message")
		return
	}
	metrics.IncrementPersisted()

	if online {
		// Try to deliver
		h.deliverToRecipient(msg)
	}

	// Send send_ok
	receivedAt := msg.CreatedAt.Unix()
	h.send(client, model.WSFrame{
		Type:       model.FrameTypeSendOK,
		MsgID:      msg.ID,
		At:         receivedAt,
		ReceivedAt: receivedAt,
		ExpiresAt:  msg.ExpiresAt.Unix(),
	})

	// Announce to federation peers (if federation is enabled)
	if h.federationManager != nil {
		h.federationManager.AnnounceMessage(msg)
	}
}

// handleAck handles the ack frame.
// `ack` marks the message delivered for the recipient identity associated with
// this WebSocket connection. The client does not provide a recipient in the
// `ack` frame; the server resolves it from tracked delivery state.
func (h *WSHandler) handleAck(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, "not authenticated")
		return
	}
	if frame.MsgID == "" {
		h.sendError(client, "msg_id required")
		return
	}

	recipientID := client.ConsumeMessageDeliveryRecipient(frame.MsgID)
	if recipientID == "" {
		// Legacy fallback: if this ack cannot be tied to a tracked delivery event,
		// write ack state under the wayfarer bucket to preserve existing behavior.
		recipientID = client.WayfarerID
	}

	if err := h.engine.AckClientDelivery(context.Background(), frame.MsgID, recipientID); err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, "failed to acknowledge message")
		return
	}
	metrics.IncrementDelivered()

	log.Printf("ws: ack msg_id=%s from=%s recipient_id=%s ttl=%ds", frame.MsgID, client.WayfarerID, recipientID, 0)

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

	limit := storeforward.NormalizePullLimit(frame.Limit)
	deliveryID := deliveryIdentityForClient(client)
	messages, err := h.engine.PullForDeliveryIdentity(context.Background(), deliveryID, limit)
	if err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, "failed to pull messages")
		return
	}

	// Convert to response wire format.
	msgs := make([]model.WSPullMessage, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		receivedAt := m.CreatedAt.Unix()
		client.TrackMessageDeliveryRecipient(m.ID, deliveryID)
		msgs = append(msgs, model.WSPullMessage{
			Message:    *m,
			ReceivedAt: receivedAt,
		})
	}

	h.send(client, model.WSFrame{
		Type:     model.FrameTypeMessages,
		Messages: msgs,
	})
}

// deliverQueuedMessages delivers any queued messages when a client connects.
func (h *WSHandler) deliverQueuedMessages(client *model.Client) {
	// Queue indexing remains wayfarer-scoped today; PullForDeliveryIdentity
	// applies per-device filtering via delivery-state checks when device_id is set.
	messages, err := h.engine.PullForDeliveryIdentity(context.Background(), deliveryIdentityForClient(client), 100)
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
		recipientID := deliveryIdentityForClient(r)
		remainingTTL := int(time.Until(msg.ExpiresAt).Seconds())
		if remainingTTL < 0 {
			remainingTTL = 0
		}
		log.Printf("ws: deliver msg_id=%s from=%s to=%s recipient_id=%s ttl=%ds", msg.ID, msg.From, r.WayfarerID, recipientID, remainingTTL)
		r.TrackMessageDeliveryRecipient(msg.ID, recipientID)
		receivedAt := msg.CreatedAt.Unix()
		h.send(r, model.WSFrame{
			Type:       model.FrameTypeMessage,
			MsgID:      msg.ID,
			From:       msg.From,
			PayloadB64: msg.Payload,
			At:         receivedAt,
			ReceivedAt: receivedAt,
		})
		// Mark as delivered to this specific recipient
		if err := h.engine.MarkDelivery(context.Background(), msg.ID, recipientID); err != nil {
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
