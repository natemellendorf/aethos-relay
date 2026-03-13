package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
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
	if devMode {
		return oc
	}

	for _, origin := range strings.Split(allowedOrigins, ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		oc.allowedOrigins[origin] = true
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

type clientSession struct {
	client            *model.Client
	adapter           *gossipv1.SessionAdapter
	handshakeComplete bool
	queueRecipient    string
	lastSummaryHave   []string
}

// WSHandler handles WebSocket connections.
type WSHandler struct {
	store             store.Store
	engine            *storeforward.Engine
	clients           *model.ClientRegistry
	relayID           string
	maxTTL            time.Duration
	originChecker     *OriginChecker
	federationManager *federation.PeerManager
	autoDeliverQueued bool
}

// NewWSHandler creates a new WebSocket handler.
// allowedOrigins is a comma-separated list of allowed origins.
// devMode enables relaxed origin checking for local development.
func NewWSHandler(store store.Store, clients *model.ClientRegistry, maxTTL time.Duration, allowedOrigins string, devMode bool, relayID string) *WSHandler {
	originChecker := NewOriginChecker(allowedOrigins, devMode)
	engine := storeforward.New(store, maxTTL)
	engine.SetAckDrivenSuppression(true)

	return &WSHandler{
		store:             store,
		engine:            engine,
		clients:           clients,
		relayID:           relayID,
		maxTTL:            maxTTL,
		originChecker:     originChecker,
		autoDeliverQueued: true,
	}
}

// SetAckDrivenSuppression keeps canonical ack-driven suppression enabled.
func (h *WSHandler) SetAckDrivenSuppression(enabled bool) {
	_ = enabled
	h.engine.SetAckDrivenSuppression(true)
	if h.federationManager != nil {
		h.federationManager.SetAckDrivenSuppression(true)
	}
}

// SetFederationManager sets the federation peer manager for relaying messages.
func (h *WSHandler) SetFederationManager(mgr *federation.PeerManager) {
	h.federationManager = mgr
	if h.federationManager != nil {
		h.federationManager.SetAckDrivenSuppression(true)
	}
}

// SetAutoDeliverQueued remains for backwards-compatible test wiring.
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
		Conn: conn,
		Send: make(chan []byte, 256),
	}
	session := &clientSession{
		client:  client,
		adapter: gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello(h.relayID), false),
	}

	go h.writePump(client)
	h.readPump(session)
}

func (h *WSHandler) readPump(session *clientSession) {
	defer func() {
		if session.client.WayfarerID != "" {
			h.clients.Unregister(session.client)
		}
		session.client.Conn.Close()
	}()

	if err := h.sendLocalHello(session.client, session.adapter); err != nil {
		log.Printf("ws: send local hello failed: %v", err)
		return
	}

	for {
		msgType, data, err := session.client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws: read error: %v", err)
			}
			return
		}
		if msgType != websocket.BinaryMessage {
			log.Printf("ws: rejected non-binary frame type=%d", msgType)
			return
		}

		metrics.IncrementReceived()
		events := session.adapter.PushInbound(data)
		if err := h.processEvents(session, events); err != nil {
			log.Printf("ws: protocol event failed: %v", err)
			return
		}
	}
}

func (h *WSHandler) processEvents(session *clientSession, events []gossipv1.Event) error {
	for _, event := range events {
		if event.Type == gossipv1.EventTypeFatal {
			return event.Err
		}

		switch event.Type {
		case gossipv1.EventTypeHelloValidated:
			if err := h.handleHelloValidated(session, event); err != nil {
				return err
			}
		case gossipv1.EventTypeSummary:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: summary before hello")
			}
			if err := h.handleSummary(session, event); err != nil {
				return err
			}
		case gossipv1.EventTypeRequest:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: request before hello")
			}
			if err := h.handleRequest(session, event); err != nil {
				return err
			}
		case gossipv1.EventTypeTransfer:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: transfer before hello")
			}
			if err := h.handleTransfer(session, event); err != nil {
				return err
			}
		case gossipv1.EventTypeReceipt:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: receipt before hello")
			}
			if err := h.handleReceipt(session, event); err != nil {
				return err
			}
		case gossipv1.EventTypeRelayIngest, gossipv1.EventTypeUntrustedRelay, gossipv1.EventTypeIgnored:
			// No-op on client path.
		}
	}

	return nil
}

func (h *WSHandler) handleHelloValidated(session *clientSession, event gossipv1.Event) error {
	if session.handshakeComplete {
		return fmt.Errorf("gossipv1: duplicate hello")
	}
	if event.Hello == nil {
		return fmt.Errorf("gossipv1: hello event missing payload")
	}

	session.client.WayfarerID = event.Hello.NodeID
	session.client.DeviceID = event.Hello.NodePubKey
	session.client.DeliveryID = storeforward.DeliveryIdentity(session.client.WayfarerID, session.client.DeviceID)
	h.clients.Register(session.client)

	session.queueRecipient = storeforward.QueueRecipient(session.client.DeliveryID)
	session.handshakeComplete = true

	have, err := h.listQueuedMessageIDs(session.queueRecipient)
	if err != nil {
		return err
	}
	session.lastSummaryHave = have

	return h.sendEnvelope(session.client, gossipv1.FrameTypeSummary, gossipv1.SummaryPayload{Have: have})
}

func (h *WSHandler) handleSummary(session *clientSession, event gossipv1.Event) error {
	if event.Summary == nil {
		return fmt.Errorf("gossipv1: summary event missing payload")
	}

	want, err := h.computeMissingWant(event.Summary.Have)
	if err != nil {
		return err
	}

	return h.sendEnvelope(session.client, gossipv1.FrameTypeRequest, gossipv1.RequestPayload{Want: want})
}

func (h *WSHandler) handleRequest(session *clientSession, event gossipv1.Event) error {
	if event.Request == nil {
		return fmt.Errorf("gossipv1: request event missing payload")
	}
	if len(event.Request.Want) == 0 {
		return nil
	}

	objects := make([]gossipv1.TransferObject, 0, len(event.Request.Want))
	expectedReceiptIDs := make([]string, 0, len(event.Request.Want))
	now := time.Now().UTC()

	for _, requestedID := range event.Request.Want {
		if uint64(len(objects)) >= gossipv1.MaxTransferItems {
			break
		}

		message, err := h.store.GetMessageByID(context.Background(), requestedID)
		if err != nil {
			if isNotFoundError(err) {
				continue
			}
			return err
		}
		if message == nil {
			continue
		}
		if message.To != session.queueRecipient {
			continue
		}
		if !message.ExpiresAt.After(now) {
			continue
		}

		objects = append(objects, gossipv1.TransferObject{
			ID:         message.ID,
			From:       message.From,
			To:         message.To,
			PayloadB64: message.Payload,
			CreatedAt:  message.CreatedAt.UTC().Unix(),
			ExpiresAt:  message.ExpiresAt.UTC().Unix(),
		})
		expectedReceiptIDs = append(expectedReceiptIDs, message.ID)
	}

	session.adapter.SetExpectedReceipt(expectedReceiptIDs)
	return h.sendEnvelope(session.client, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: objects})
}

func (h *WSHandler) handleTransfer(session *clientSession, event gossipv1.Event) error {
	if event.Transfer == nil {
		return fmt.Errorf("gossipv1: transfer event missing payload")
	}

	receipt := gossipv1.ReceiptPayload{
		Accepted: make([]string, 0, len(event.Transfer.Objects)),
		Rejected: append([]gossipv1.TransferObjectRejection(nil), event.Transfer.Rejected...),
	}

	for _, indexed := range event.Transfer.Objects {
		validationErr := validateTransferObject(indexed.Object)
		if validationErr != nil {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: validationErr.Error(),
			})
			continue
		}

		existing, err := h.store.GetMessageByID(context.Background(), indexed.Object.ID)
		if err != nil && !isNotFoundError(err) {
			return err
		}
		if err == nil && existing != nil {
			receipt.Accepted = append(receipt.Accepted, indexed.Object.ID)
			continue
		}

		decodedPayload, err := model.DecodePayloadB64(indexed.Object.PayloadB64)
		if err != nil {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: "invalid payload_b64",
			})
			continue
		}

		createdAt := time.Unix(indexed.Object.CreatedAt, 0).UTC()
		expiresAt := time.Unix(indexed.Object.ExpiresAt, 0).UTC()
		if !expiresAt.After(createdAt) {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: "expires_at must be greater than created_at",
			})
			continue
		}
		if !expiresAt.After(time.Now().UTC()) {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: "object already expired",
			})
			continue
		}

		message := &model.Message{
			ID:        indexed.Object.ID,
			From:      indexed.Object.From,
			To:        indexed.Object.To,
			Payload:   model.EncodePayloadB64(decodedPayload, model.PayloadEncodingPrefBase64URL),
			CreatedAt: createdAt,
			ExpiresAt: expiresAt,
		}

		if err := h.engine.PersistMessage(context.Background(), message); err != nil {
			metrics.IncrementStoreErrors()
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: "persist failed",
			})
			continue
		}

		metrics.IncrementPersisted()
		receipt.Accepted = append(receipt.Accepted, indexed.Object.ID)
	}

	if len(receipt.Accepted) > 1 {
		sort.Strings(receipt.Accepted)
	}

	return h.sendEnvelope(session.client, gossipv1.FrameTypeReceipt, receipt)
}

func (h *WSHandler) handleReceipt(session *clientSession, event gossipv1.Event) error {
	if event.Receipt == nil {
		return fmt.Errorf("gossipv1: receipt event missing payload")
	}

	for _, acceptedID := range event.Receipt.Accepted {
		transitioned, err := h.engine.AckClientDelivery(context.Background(), acceptedID, session.client.DeliveryID)
		if err != nil {
			metrics.IncrementStoreErrors()
			return err
		}
		if transitioned {
			metrics.IncrementDelivered()
		}
	}

	return nil
}

func (h *WSHandler) listQueuedMessageIDs(queueRecipient string) ([]string, error) {
	ids, err := h.store.GetAllQueuedMessageIDs(context.Background(), queueRecipient)
	if err != nil {
		return nil, err
	}

	if len(ids) <= 1 {
		return ids, nil
	}

	seen := make(map[string]struct{}, len(ids))
	deduped := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	sort.Strings(deduped)

	return deduped, nil
}

func (h *WSHandler) computeMissingWant(summaryHave []string) ([]string, error) {
	if len(summaryHave) == 0 {
		return []string{}, nil
	}

	want := make([]string, 0, len(summaryHave))
	for _, id := range summaryHave {
		if id == "" {
			continue
		}

		message, err := h.store.GetMessageByID(context.Background(), id)
		if err != nil {
			if isNotFoundError(err) {
				want = append(want, id)
				continue
			}
			return nil, err
		}
		if message == nil {
			want = append(want, id)
		}
	}

	if len(want) > 1 {
		sort.Strings(want)
	}
	if uint64(len(want)) > gossipv1.MaxWantItems {
		want = want[:gossipv1.MaxWantItems]
	}

	return want, nil
}

func validateTransferObject(object gossipv1.TransferObject) error {
	if object.ID == "" {
		return fmt.Errorf("id is required")
	}
	if object.From == "" {
		return fmt.Errorf("from is required")
	}
	if object.To == "" {
		return fmt.Errorf("to is required")
	}
	if object.PayloadB64 == "" {
		return fmt.Errorf("payload_b64 is required")
	}
	if object.CreatedAt <= 0 {
		return fmt.Errorf("created_at must be > 0")
	}
	if object.ExpiresAt <= object.CreatedAt {
		return fmt.Errorf("expires_at must be greater than created_at")
	}
	return nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
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
			if err := client.Conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *WSHandler) sendLocalHello(client *model.Client, adapter *gossipv1.SessionAdapter) error {
	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		return err
	}
	h.sendBinary(client, helloBytes)
	return nil
}

func (h *WSHandler) sendEnvelope(client *model.Client, frameType string, payload any) error {
	frame, err := gossipv1.EncodeEnvelope(frameType, payload)
	if err != nil {
		return err
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		return err
	}
	h.sendBinary(client, prefixed)
	return nil
}

// sendBinary sends a frame to the client with bounded backpressure.
func (h *WSHandler) sendBinary(client *model.Client, data []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ws: failed to send, channel closed for client %s", client.WayfarerID)
			metrics.IncrementDropped()
		}
	}()

	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()

	select {
	case client.Send <- data:
	case <-timer.C:
		log.Printf("ws: failed to send, channel full after timeout for client %s", client.WayfarerID)
		metrics.IncrementDropped()
	}
}
