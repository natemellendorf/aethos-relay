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
	"github.com/natemellendorf/aethos-relay/internal/protocolcompat/clientv1"
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

// SetAckDrivenSuppression enables canonical ack-driven suppression semantics.
// When disabled, relay keeps legacy mark-on-push suppression behavior.
func (h *WSHandler) SetAckDrivenSuppression(enabled bool) {
	h.engine.SetAckDrivenSuppression(enabled)
	if h.federationManager != nil {
		h.federationManager.SetAckDrivenSuppression(enabled)
	}
}

// SetFederationManager sets the federation peer manager for relaying messages.
func (h *WSHandler) SetFederationManager(mgr *federation.PeerManager) {
	h.federationManager = mgr
	if h.federationManager != nil {
		h.federationManager.SetAckDrivenSuppression(h.engine.IsAckDrivenSuppression())
	}
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
	client.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64)

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
		h.sendError(client, model.ErrorCodeInvalidPayload, "relay frame type not allowed on client connections")
		return
	}

	switch frame.Type {
	case model.FrameTypeHello:
		h.handleHello(client, frame)
	case model.FrameTypeSend:
		h.handleSend(client, frame)
	case model.FrameTypeAck:
		// `ack` marks the message delivered for this connection's recipient identity.
		h.handleAck(client, frame)
	case model.FrameTypePull:
		h.handlePull(client, frame)
	default:
		h.sendError(client, model.ErrorCodeInvalidPayload, "unknown frame type")
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
		h.sendError(client, model.ErrorCodeInvalidWayfarerID, "wayfarer_id required")
		return
	}

	// Legacy clients omit device_id; in that case, keep the delivery identity at
	// the existing wayfarer-only bucket for backward compatibility.
	deliveryID := clientv1.DeliveryIdentity(frame.WayfarerID, frame.DeviceID)

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
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}
	if frame.To == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "recipient required")
		return
	}
	if frame.PayloadB64 == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "payload required")
		return
	}

	normalizedPayloadB64 := model.NormalizePayloadB64(frame.PayloadB64)
	if _, err := model.DecodePayloadB64(normalizedPayloadB64); err != nil {
		log.Printf("ws: rejecting send from=%s to=%s: invalid payload_b64: %v", client.WayfarerID, frame.To, err)
		h.sendError(client, model.ErrorCodeInvalidPayload, "invalid payload_b64")
		return
	}

	client.SetPayloadEncodingPref(model.DetectPayloadB64EncodingPref(normalizedPayloadB64))

	msg, ttl := h.engine.AcceptClientSend(client.WayfarerID, frame.To, normalizedPayloadB64, frame.TTLSeconds)

	// Check if recipient is online
	online := h.clients.IsOnline(frame.To)
	log.Printf("ws: send msg_id=%s from=%s to=%s ttl=%ds online=%t", msg.ID, msg.From, msg.To, int(ttl.Seconds()), online)

	if err := h.engine.PersistMessage(context.Background(), msg); err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, model.ErrorCodeInternalError, "failed to persist message")
		return
	}
	metrics.IncrementPersisted()

	if online {
		// Try to deliver
		h.deliverToRecipient(msg)
	}

	// Send send_ok after durable persistence.
	h.send(client, clientv1.EncodeSendOK(msg))

	// Announce to federation peers (if federation is enabled)
	if h.federationManager != nil {
		h.federationManager.AnnounceMessage(msg)
	}
}

// handleAck handles the ack frame.
// `ack` marks the message delivered for a resolved recipient identity.
// The client does not provide a recipient in the `ack` frame; the server uses
// compat resolution (tracked recipient identity first, then connection
// delivery identity fallback).
func (h *WSHandler) handleAck(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}
	if frame.MsgID == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "msg_id required")
		return
	}

	recipientID := clientv1.ResolveAckRecipient(client, frame.MsgID)

	transitioned, err := h.engine.AckClientDelivery(context.Background(), frame.MsgID, recipientID)
	if err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, model.ErrorCodeInternalError, "failed to acknowledge message")
		return
	}
	if transitioned {
		metrics.IncrementDelivered()
	}

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
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}

	limit := storeforward.NormalizePullLimit(frame.Limit)
	deliveryID := deliveryIdentityForClient(client)
	messages, err := h.engine.PullForDeliveryIdentity(context.Background(), deliveryID, limit)
	if err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, model.ErrorCodeInternalError, "failed to pull messages")
		return
	}

	// Convert to response wire format.
	msgs := make([]model.WSPullMessage, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		decodedPayload, err := model.DecodePayloadB64(m.Payload)
		if err != nil {
			h.dropCorruptMessage(m.ID, deliveryID, "pull", err)
			continue
		}
		wireMessage := *m
		wireMessage.Payload = model.EncodePayloadB64(decodedPayload, client.GetPayloadEncodingPref())
		client.TrackMessageDeliveryRecipient(wireMessage.ID, deliveryID)
		msgs = append(msgs, clientv1.EncodePullEntry(&wireMessage))
	}

	h.send(client, model.WSFrame{
		Type:     model.FrameTypeMessages,
		Messages: msgs,
	})
}

// deliverQueuedMessages delivers any queued messages when a client connects.
func (h *WSHandler) deliverQueuedMessages(client *model.Client) {
	// Queue indexing remains wayfarer-scoped today; PullForDeliveryIdentity
	// applies per-device filtering via suppression state checks when device_id is set.
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
	decodedPayload, err := model.DecodePayloadB64(msg.Payload)
	if err != nil {
		h.dropCorruptMessage(msg.ID, msg.To, "deliver", err)
		return
	}

	recipients := h.clients.GetClients(msg.To)
	for _, r := range recipients {
		recipientID := deliveryIdentityForClient(r)
		payloadB64 := model.EncodePayloadB64(decodedPayload, r.GetPayloadEncodingPref())
		remainingTTL := int(time.Until(msg.ExpiresAt).Seconds())
		if remainingTTL < 0 {
			remainingTTL = 0
		}
		log.Printf("ws: deliver msg_id=%s from=%s to=%s recipient_id=%s ttl=%ds", msg.ID, msg.From, r.WayfarerID, recipientID, remainingTTL)
		r.TrackMessageDeliveryRecipient(msg.ID, recipientID)
		wireMessage := *msg
		wireMessage.Payload = payloadB64
		h.send(r, clientv1.EncodePushMessage(&wireMessage))
		if !h.engine.IsAckDrivenSuppression() {
			alreadyDelivered, err := h.store.IsDeliveredTo(context.Background(), msg.ID, recipientID)
			if err != nil {
				metrics.IncrementStoreErrors()
				log.Printf("ws: failed to check prior delivery state: %v", err)
				continue
			}
			if alreadyDelivered {
				continue
			}

			// Legacy suppression path: mark on push so reconnect/pull flows continue
			// to suppress as before until canonical mode is enabled.
			if err := h.engine.MarkDelivery(context.Background(), msg.ID, recipientID); err != nil {
				metrics.IncrementStoreErrors()
				log.Printf("ws: failed to mark delivered: %v", err)
				continue
			}
			metrics.IncrementDelivered()
		}
	}
}

func (h *WSHandler) dropCorruptMessage(msgID, recipientID, stage string, decodeErr error) {
	if msgID == "" {
		log.Printf("ws: skipping corrupt payload removal with empty msg_id recipient=%s stage=%s: %v", recipientID, stage, decodeErr)
		return
	}

	log.Printf("ws: dropping corrupt payload msg_id=%s recipient=%s stage=%s: %v", msgID, recipientID, stage, decodeErr)
	if err := h.engine.RemoveMessage(context.Background(), msgID); err != nil {
		metrics.IncrementStoreErrors()
		log.Printf("ws: failed to remove corrupt message msg_id=%s stage=%s: %v", msgID, stage, err)
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

// sendError sends canonical error fields (code/message) and temporarily mirrors
// message into legacy msg_id for backward compatibility during migration.
func (h *WSHandler) sendError(client *model.Client, code model.ErrorCode, message string) {
	if code == "" {
		code = model.ErrorCodeInternalError
	}
	if message == "" {
		if code == model.ErrorCodeInternalError {
			message = "internal error"
		} else {
			message = string(code)
		}
	}
	legacyShape := clientv1.EncodeError(message)

	h.send(client, model.WSFrame{
		Type:    legacyShape.Type,
		Code:    string(code),
		Message: message,
		MsgID:   legacyShape.MsgID,
	})
}
