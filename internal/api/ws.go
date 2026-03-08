package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/protocol/envelopev1"
	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

const (
	strictClientRelayV1EnvName  = "AETHOS_CLIENT_RELAY_STRICT_V1"
	strictClientDefaultTTL      = 3600
	writerSendTimeout           = 1 * time.Second
	strictWayfarerIDHexLen      = 64
	strictWayfarerIDPatternExpr = "^[a-f0-9]{64}$"
)

var strictWayfarerIDPattern = regexp.MustCompile(strictWayfarerIDPatternExpr)

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
	if devMode {
		return oc
	}
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
	if oc.devMode {
		return true
	}
	if len(oc.allowedOrigins) == 0 {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return oc.allowedOrigins[origin]
}

// WSHandler handles WebSocket connections.
type WSHandler struct {
	store               store.Store
	engine              *storeforward.Engine
	clients             *model.ClientRegistry
	relayID             string
	maxTTL              time.Duration
	originChecker       *OriginChecker
	federationManager   *federation.PeerManager
	autoDeliverQueued   bool
	strictClientRelayV1 bool
}

// NewWSHandler creates a new WebSocket handler.
// allowedOrigins is a comma-separated list of allowed origins.
// devMode enables relaxed origin checking for local development.
func NewWSHandler(store store.Store, clients *model.ClientRegistry, maxTTL time.Duration, allowedOrigins string, devMode bool, relayID string) *WSHandler {
	originChecker := NewOriginChecker(allowedOrigins, devMode)
	engine := storeforward.New(store, maxTTL)
	return &WSHandler{
		store:               store,
		engine:              engine,
		clients:             clients,
		relayID:             relayID,
		maxTTL:              maxTTL,
		originChecker:       originChecker,
		autoDeliverQueued:   true,
		strictClientRelayV1: strictClientRelayV1EnabledFromEnv(),
	}
}

// SetStrictClientRelayV1 sets strict canonical-only client protocol mode.
func (h *WSHandler) SetStrictClientRelayV1(enabled bool) {
	h.strictClientRelayV1 = enabled
}

// IsStrictClientRelayV1 reports whether strict client protocol mode is enabled.
func (h *WSHandler) IsStrictClientRelayV1() bool {
	return h.strictClientRelayV1
}

// SetAckDrivenSuppression toggles canonical ack-driven suppression semantics.
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
	if !h.originChecker.Check(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
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
	if h.strictClientRelayV1 {
		client.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64URL)
	} else {
		client.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64)
	}

	go h.writePump(client)
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

		if keepOpen := h.handleFrame(client, &frame); !keepOpen {
			break
		}
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
func (h *WSHandler) handleFrame(client *model.Client, frame *model.WSFrame) bool {
	metrics.IncrementReceived()

	if h.strictClientRelayV1 && client.WayfarerID == "" && frame.Type != model.FrameTypeHello {
		h.sendError(client, model.ErrorCodeInvalidPayload, "hello required before other frames")
		return false
	}

	if model.IsRelayFrameType(frame.Type) {
		h.sendError(client, model.ErrorCodeInvalidPayload, "relay frame type not allowed on client connections")
		return true
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
		h.sendError(client, model.ErrorCodeInvalidPayload, "unknown frame type")
	}

	return true
}

func deliveryIdentityForClientCompat(client *model.Client) string {
	if client == nil {
		return ""
	}
	if client.DeliveryID != "" {
		return client.DeliveryID
	}
	return client.WayfarerID
}

func deliveryIdentityForClientStrict(client *model.Client) string {
	if client == nil {
		return ""
	}
	if client.WayfarerID == "" || client.DeviceID == "" {
		return ""
	}
	return storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)
}

// handleHello handles the hello frame.
func (h *WSHandler) handleHello(client *model.Client, frame *model.WSFrame) {
	if frame.WayfarerID == "" {
		h.sendError(client, model.ErrorCodeInvalidWayfarerID, "wayfarer_id required")
		return
	}
	if h.strictClientRelayV1 && !isStrictWayfarerID(frame.WayfarerID) {
		h.sendError(client, model.ErrorCodeInvalidWayfarerID, "wayfarer_id must be 64 lowercase hex")
		return
	}
	if h.strictClientRelayV1 && frame.DeviceID == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "device_id required")
		return
	}

	deliveryID := storeforward.DeliveryIdentity(frame.WayfarerID, frame.DeviceID)

	client.WayfarerID = frame.WayfarerID
	client.DeviceID = frame.DeviceID
	client.DeliveryID = deliveryID
	client.ResetDeliveryTracking()
	client.ID = uuid.New().String()
	h.clients.Register(client)

	h.send(client, model.WSFrame{Type: model.FrameTypeHelloOK, RelayID: h.relayID})

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
	if h.strictClientRelayV1 && !isStrictWayfarerID(frame.To) {
		h.sendError(client, model.ErrorCodeInvalidWayfarerID, "to must be 64 lowercase hex")
		return
	}
	if frame.PayloadB64 == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "payload required")
		return
	}

	payloadB64 := frame.PayloadB64
	if !h.strictClientRelayV1 {
		payloadB64 = model.NormalizePayloadB64(payloadB64)
	}
	decodedPayload, err := h.decodeClientPayload(payloadB64)
	if err != nil {
		log.Printf("ws: rejecting send from=%s to=%s: invalid payload_b64: %v", client.WayfarerID, frame.To, err)
		h.sendError(client, model.ErrorCodeInvalidPayload, "invalid payload_b64")
		return
	}

	if h.strictClientRelayV1 {
		payloadRecipient, err := envelopev1.RecipientWayfarerID(decodedPayload)
		if err != nil {
			log.Printf("ws: rejecting send from=%s to=%s: cannot decode envelope recipient: %v", client.WayfarerID, frame.To, err)
			h.sendError(client, model.ErrorCodeInvalidPayload, "invalid EnvelopeV1 payload")
			return
		}
		if payloadRecipient != frame.To {
			h.sendError(client, model.ErrorCodeToMismatch, "send.to does not match EnvelopeV1 recipient")
			return
		}
	} else {
		client.SetPayloadEncodingPref(model.DetectPayloadB64EncodingPref(payloadB64))
	}

	ttlSeconds := frame.TTLSeconds
	if h.strictClientRelayV1 && ttlSeconds <= 0 {
		ttlSeconds = strictClientDefaultTTL
	}

	msg, ttl := h.engine.AcceptClientSend(client.WayfarerID, frame.To, payloadB64, ttlSeconds)

	online := h.clients.IsOnline(frame.To)
	log.Printf("ws: send msg_id=%s from=%s to=%s ttl=%ds online=%t", msg.ID, msg.From, msg.To, int(ttl.Seconds()), online)

	if err := h.engine.PersistMessage(context.Background(), msg); err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, model.ErrorCodeInternalError, "failed to persist message")
		return
	}
	metrics.IncrementPersisted()

	if online {
		h.deliverToRecipient(msg)
	}

	h.send(client, h.encodeSendOK(msg))

	if h.federationManager != nil {
		h.federationManager.AnnounceMessage(msg)
	}
}

// handleAck handles the ack frame.
func (h *WSHandler) handleAck(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}
	if frame.MsgID == "" {
		h.sendError(client, model.ErrorCodeInvalidPayload, "msg_id required")
		return
	}

	var recipientID string
	if h.strictClientRelayV1 {
		if client.DeviceID == "" {
			h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
			return
		}
		recipientID = deliveryIdentityForClientStrict(client)
	} else {
		recipientID = resolveAckRecipientCompat(client, frame.MsgID)
	}

	if recipientID == "" {
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}

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

	h.send(client, model.WSFrame{Type: model.FrameTypeAckOK, MsgID: frame.MsgID})
}

// handlePull handles the pull frame.
func (h *WSHandler) handlePull(client *model.Client, frame *model.WSFrame) {
	if client.WayfarerID == "" {
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}

	limit := storeforward.NormalizePullLimit(frame.Limit)
	var deliveryID string
	if h.strictClientRelayV1 {
		deliveryID = deliveryIdentityForClientStrict(client)
	} else {
		deliveryID = deliveryIdentityForClientCompat(client)
	}
	if deliveryID == "" {
		h.sendError(client, model.ErrorCodeAuthFailed, "not authenticated")
		return
	}

	messages, err := h.engine.PullForDeliveryIdentity(context.Background(), deliveryID, limit)
	if err != nil {
		metrics.IncrementStoreErrors()
		h.sendError(client, model.ErrorCodeInternalError, "failed to pull messages")
		return
	}

	msgEntries := make([]model.WSPullMessage, 0, len(messages))
	payloadPref := h.payloadEncodingPrefForClient(client)
	for _, m := range messages {
		if m == nil {
			continue
		}
		if !h.isDeliverable(m) {
			h.dropExpiredMessage(m.ID, deliveryID, "pull", m.ExpiresAt)
			continue
		}

		decodedPayload, err := model.DecodePayloadB64(m.Payload)
		if err != nil {
			h.dropCorruptMessage(m.ID, deliveryID, "pull", err)
			continue
		}
		payloadB64 := model.EncodePayloadB64(decodedPayload, payloadPref)
		if h.strictClientRelayV1 {
			msgEntries = append(msgEntries, model.WSPullMessage{
				MsgID:      m.ID,
				From:       m.From,
				PayloadB64: payloadB64,
				ReceivedAt: m.CreatedAt.Unix(),
			})
			continue
		}

		client.TrackMessageDeliveryRecipient(m.ID, deliveryID)
		receivedAt := m.CreatedAt.Unix()
		msgEntries = append(msgEntries, model.WSPullMessage{
			MsgID:      m.ID,
			From:       m.From,
			To:         m.To,
			PayloadB64: payloadB64,
			At:         receivedAt,
			ReceivedAt: receivedAt,
			ExpiresAt:  m.ExpiresAt.Unix(),
		})
	}

	h.send(client, model.WSFrame{Type: model.FrameTypeMessages, Messages: msgEntries})
}

// deliverQueuedMessages delivers any queued messages when a client connects.
func (h *WSHandler) deliverQueuedMessages(client *model.Client) {
	var deliveryID string
	if h.strictClientRelayV1 {
		deliveryID = deliveryIdentityForClientStrict(client)
	} else {
		deliveryID = deliveryIdentityForClientCompat(client)
	}
	if deliveryID == "" {
		return
	}

	messages, err := h.engine.PullForDeliveryIdentity(context.Background(), deliveryID, 100)
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
	if msg == nil {
		return
	}
	if !h.isDeliverable(msg) {
		h.dropExpiredMessage(msg.ID, msg.To, "deliver", msg.ExpiresAt)
		return
	}

	decodedPayload, err := model.DecodePayloadB64(msg.Payload)
	if err != nil {
		h.dropCorruptMessage(msg.ID, msg.To, "deliver", err)
		return
	}

	recipients := h.clients.GetClients(msg.To)
	for _, recipient := range recipients {
		var recipientID string
		if h.strictClientRelayV1 {
			recipientID = deliveryIdentityForClientStrict(recipient)
		} else {
			recipientID = deliveryIdentityForClientCompat(recipient)
		}
		if recipientID == "" {
			continue
		}

		payloadB64 := model.EncodePayloadB64(decodedPayload, h.payloadEncodingPrefForClient(recipient))
		remainingTTL := int(time.Until(msg.ExpiresAt).Seconds())
		if remainingTTL < 0 {
			remainingTTL = 0
		}
		log.Printf("ws: deliver msg_id=%s from=%s to=%s recipient_id=%s ttl=%ds", msg.ID, msg.From, recipient.WayfarerID, recipientID, remainingTTL)

		if !h.strictClientRelayV1 {
			recipient.TrackMessageDeliveryRecipient(msg.ID, recipientID)
		}
		h.send(recipient, h.encodePushMessage(msg, payloadB64))

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

func (h *WSHandler) dropExpiredMessage(msgID, recipientID, stage string, expiresAt time.Time) {
	if msgID == "" {
		return
	}

	log.Printf("ws: dropping expired message msg_id=%s recipient=%s stage=%s expires_at=%d", msgID, recipientID, stage, expiresAt.Unix())
	if err := h.engine.RemoveMessage(context.Background(), msgID); err != nil {
		metrics.IncrementStoreErrors()
		log.Printf("ws: failed to remove expired message msg_id=%s stage=%s: %v", msgID, stage, err)
	}
}

// send sends a frame to the client with backpressure handling.
func (h *WSHandler) send(client *model.Client, frame model.WSFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("ws: failed to marshal frame: %v", err)
		return
	}
	select {
	case client.Send <- data:
	case <-time.After(writerSendTimeout):
		log.Printf("ws: failed to send, channel full after timeout for client %s", client.WayfarerID)
		metrics.IncrementDropped()
	}
}

// sendError sends protocol-compliant error fields for strict and compatibility modes.
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

	frame := model.WSFrame{
		Type:    model.FrameTypeError,
		Code:    string(code),
		Message: message,
	}
	if !h.strictClientRelayV1 {
		frame.MsgID = message
	}

	h.send(client, frame)
}

func (h *WSHandler) decodeClientPayload(payloadB64 string) ([]byte, error) {
	if h.strictClientRelayV1 {
		return model.DecodePayloadB64Canonical(payloadB64)
	}
	return model.DecodePayloadB64(payloadB64)
}

func (h *WSHandler) isDeliverable(msg *model.Message) bool {
	if msg == nil {
		return false
	}
	if !h.strictClientRelayV1 {
		return true
	}
	nowSeconds := time.Now().Unix()
	return nowSeconds < msg.ExpiresAt.Unix()
}

func (h *WSHandler) payloadEncodingPrefForClient(client *model.Client) model.PayloadEncodingPref {
	if h.strictClientRelayV1 {
		return model.PayloadEncodingPrefBase64URL
	}
	if client == nil {
		return model.PayloadEncodingPrefBase64
	}
	return client.GetPayloadEncodingPref()
}

func (h *WSHandler) encodeSendOK(msg *model.Message) model.WSFrame {
	if msg == nil {
		return model.WSFrame{Type: model.FrameTypeSendOK}
	}
	receivedAt := msg.CreatedAt.Unix()
	if h.strictClientRelayV1 {
		return model.WSFrame{
			Type:       model.FrameTypeSendOK,
			MsgID:      msg.ID,
			ReceivedAt: receivedAt,
			ExpiresAt:  msg.ExpiresAt.Unix(),
		}
	}
	return model.WSFrame{
		Type:       model.FrameTypeSendOK,
		MsgID:      msg.ID,
		At:         receivedAt,
		ReceivedAt: receivedAt,
		ExpiresAt:  msg.ExpiresAt.Unix(),
	}
}

func (h *WSHandler) encodePushMessage(msg *model.Message, payloadB64 string) model.WSFrame {
	if msg == nil {
		return model.WSFrame{Type: model.FrameTypeMessage}
	}
	receivedAt := msg.CreatedAt.Unix()
	if h.strictClientRelayV1 {
		return model.WSFrame{
			Type:       model.FrameTypeMessage,
			MsgID:      msg.ID,
			From:       msg.From,
			PayloadB64: payloadB64,
			ReceivedAt: receivedAt,
		}
	}
	return model.WSFrame{
		Type:       model.FrameTypeMessage,
		MsgID:      msg.ID,
		From:       msg.From,
		PayloadB64: payloadB64,
		At:         receivedAt,
		ReceivedAt: receivedAt,
	}
}

func resolveAckRecipientCompat(client *model.Client, msgID string) string {
	if client == nil {
		return ""
	}
	if recipientID := client.MessageDeliveryRecipient(msgID); recipientID != "" {
		return recipientID
	}
	if client.DeliveryID != "" {
		return client.DeliveryID
	}
	return client.WayfarerID
}

func strictClientRelayV1EnabledFromEnv() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(strictClientRelayV1EnvName)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isStrictWayfarerID(value string) bool {
	if len(value) != strictWayfarerIDHexLen {
		return false
	}
	return strictWayfarerIDPattern.MatchString(value)
}
