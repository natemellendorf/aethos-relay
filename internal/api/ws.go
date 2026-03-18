package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
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
	sessionID         string
	peerLabel         string
	handshakeComplete bool
	queueRecipient    string
	summaryCursor     string
	peerMaxWant       uint64
}

const wsDebugItemSampleLimit = 4

// WSHandler handles WebSocket connections.
type WSHandler struct {
	store             store.Store
	envelopeStore     store.EnvelopeStore
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

// SetEnvelopeStore configures canonical gossip object inventory for summaries.
func (h *WSHandler) SetEnvelopeStore(envelopeStore store.EnvelopeStore) {
	h.envelopeStore = envelopeStore
	h.engine.ConfigureFederation(h.relayID, envelopeStore)
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
	sessionID := gossipv1.NewDebugSessionID("ws")
	peerLabel := r.RemoteAddr
	debug := gossipv1.NewDebugSessionLogger(sessionID, peerLabel)
	debug.Log("in", "CONNECTION", "session_state", "state", "accepted_upgrade", "http_remote_addr", r.RemoteAddr)
	session := &clientSession{
		client:    client,
		adapter:   gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello(h.relayID), false),
		sessionID: sessionID,
		peerLabel: peerLabel,
	}

	go h.writePump(session)
	h.readPump(session)
}

func (h *WSHandler) readPump(session *clientSession) {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)
	debug.Log("local", "SESSION", "session_state", "state", "read_pump_started")

	defer func() {
		debug.Log("local", "SESSION", "session_state", "state", "read_pump_stopped")
		if session.client.WayfarerID != "" {
			h.clients.Unregister(session.client)
		}
		session.client.Conn.Close()
	}()

	if err := h.sendLocalHello(session, session.adapter); err != nil {
		log.Printf("ws: send local hello failed: %v", err)
		debug.Log("out", gossipv1.FrameTypeHello, "send_failed", "transport_ok", false, "protocol_ok", false, "err", err)
		return
	}

	for {
		msgType, data, err := session.client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws: read error: %v", err)
			}
			debug.Log("in", "CONNECTION", "recv_failed", "transport_ok", false, "protocol_ok", false, "close_reason", err, "err", err)
			return
		}
		if msgType != websocket.BinaryMessage {
			log.Printf("ws: rejected non-binary frame type=%d", msgType)
			debug.Log("in", "NON_BINARY", "recv_rejected", "msg_type_raw", msgType, "bytes_in", len(data), "transport_ok", true, "protocol_ok", false)
			return
		}
		debug.Log("in", gossipv1.FrameTypeFromPrefixedBinaryFrame(data), "received", "bytes_in", len(data), "transport_ok", true)

		metrics.IncrementReceived()
		events := session.adapter.PushInbound(data)
		if err := h.processEvents(session, events); err != nil {
			log.Printf("ws: protocol event failed: %v", err)
			debug.Log("in", session.adapter.LastFrameType(), "process_failed", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", err)
			return
		}
	}
}

func (h *WSHandler) processEvents(session *clientSession, events []gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

	for _, event := range events {
		if event.Type == gossipv1.EventTypeFatal {
			debug.Log("in", session.adapter.LastFrameType(), "protocol_fatal", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", event.Err)
			return event.Err
		}

		switch event.Type {
		case gossipv1.EventTypeHelloValidated:
			if event.Hello != nil {
				debug.Log("in", gossipv1.FrameTypeHello, "hello_received", "transport_ok", true, "protocol_ok", true, "remote_node_id", event.Hello.NodeID)
				session.peerMaxWant = event.Hello.MaxWant
			}
			if err := h.handleHelloValidated(session, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeHello, "hello_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
			debug.Log("local", "SESSION", "session_state", "state", "hello_validated")
		case gossipv1.EventTypeSummary:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: summary before hello")
			}
			if event.Summary != nil {
				count, sample, hash := gossipv1.ItemIDSample(event.Summary.PreviewItemIDs, wsDebugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeSummary, "summary_received", "transport_ok", true, "protocol_ok", true, "item_count", event.Summary.ItemCount, "bloom_bytes", len(event.Summary.BloomFilter), "preview_count", count, "item_ids_sample", sample, "item_ids_hash", hash, "preview_cursor", event.Summary.PreviewCursor)
			}
			if err := h.handleSummary(session, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeSummary, "summary_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeRequest:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: request before hello")
			}
			if event.Request != nil {
				count, sample, hash := gossipv1.ItemIDSample(event.Request.Want, wsDebugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeRequest, "request_received", "transport_ok", true, "protocol_ok", true, "requested_count", count, "item_ids_sample", sample, "item_ids_hash", hash)
			}
			if err := h.handleRequest(session, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeRequest, "request_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeTransfer:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: transfer before hello")
			}
			if event.Transfer != nil {
				transferIDs := make([]string, 0, len(event.Transfer.Objects))
				for _, indexed := range event.Transfer.Objects {
					transferIDs = append(transferIDs, indexed.Object.ItemID)
				}
				count, sample, hash := gossipv1.ItemIDSample(transferIDs, wsDebugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_received", "transport_ok", true, "protocol_ok", true, "object_count", count, "item_ids_sample", sample, "item_ids_hash", hash, "parse_rejected_count", len(event.Transfer.Rejected))
			}
			if err := h.handleTransfer(session, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeReceipt:
			if !session.handshakeComplete {
				return fmt.Errorf("gossipv1: receipt before hello")
			}
			if event.Receipt != nil {
				acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(event.Receipt.Accepted, wsDebugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_received", "transport_ok", true, "protocol_ok", true, "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash)
			}
			if err := h.handleReceipt(session, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeRelayIngest, gossipv1.EventTypeUntrustedRelay, gossipv1.EventTypeIgnored:
			debug.Log("in", event.FrameType, "frame_ignored", "protocol_ok", true)
		}
	}

	return nil
}

func (h *WSHandler) handleHelloValidated(session *clientSession, event gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

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
	session.peerLabel = session.client.WayfarerID
	debug = gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)
	debug.Log("local", gossipv1.FrameTypeHello, "identity_bound", "wayfarer_id", session.client.WayfarerID, "device_id", session.client.DeviceID)

	session.queueRecipient = storeforward.QueueRecipient(session.client.DeliveryID)
	session.handshakeComplete = true
	debug.Log("local", "SESSION", "session_state", "state", "handshake_complete", "queue_recipient", session.queueRecipient)

	summary, nextCursor, err := h.buildRecipientSummary(session.queueRecipient, session.summaryCursor)
	if err != nil {
		debug.Log("local", gossipv1.FrameTypeSummary, "summary_inventory_failed", "store_ok", false, "err", err)
		return err
	}
	session.summaryCursor = nextCursor
	previewCount, previewSample, previewHash := gossipv1.ItemIDSample(summary.PreviewItemIDs, wsDebugItemSampleLimit)
	debug.Log(
		"out",
		gossipv1.FrameTypeSummary,
		"summary_constructed",
		"summary_path", "ws.handleHelloValidated:envelope_store_by_destination->summary_preview",
		"item_count", summary.ItemCount,
		"bloom_bytes", len(summary.BloomFilter),
		"preview_count", previewCount,
		"item_ids_sample", previewSample,
		"item_ids_hash", previewHash,
		"preview_cursor", summary.PreviewCursor,
		"next_preview_cursor", nextCursor,
	)

	return h.sendEnvelope(session, gossipv1.FrameTypeSummary, summary)
}

func (h *WSHandler) handleSummary(session *clientSession, event gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

	if event.Summary == nil {
		return fmt.Errorf("gossipv1: summary event missing payload")
	}
	if err := validateSummaryPreviewCursor(event.Summary.PreviewItemIDs, event.Summary.PreviewCursor); err != nil {
		return err
	}

	want, err := h.computeMissingWant(event.Summary, nil, session.peerMaxWant, debug)
	if err != nil {
		debug.Log("local", gossipv1.FrameTypeSummary, "summary_compute_want_failed", "store_ok", false, "err", err)
		return err
	}
	wantCount, wantSample, wantHash := gossipv1.ItemIDSample(want, wsDebugItemSampleLimit)
	debug.Log("local", gossipv1.FrameTypeSummary, "summary_computed_want", "requested_count", wantCount, "item_ids_sample", wantSample, "item_ids_hash", wantHash, "store_ok", true)

	return h.sendEnvelope(session, gossipv1.FrameTypeRequest, gossipv1.RequestPayload{Want: want})
}

func (h *WSHandler) handleRequest(session *clientSession, event gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

	if event.Request == nil {
		return fmt.Errorf("gossipv1: request event missing payload")
	}
	if len(event.Request.Want) == 0 {
		debug.Log("in", gossipv1.FrameTypeRequest, "request_empty", "requested_count", 0, "protocol_ok", true)
		return nil
	}

	objects := make([]gossipv1.TransferObject, 0, len(event.Request.Want))
	expectedReceiptIDs := make([]string, 0, len(event.Request.Want))
	now := time.Now().UTC()

	for _, requestedID := range event.Request.Want {
		if uint64(len(objects)) >= gossipv1.MaxTransferItems {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, requestedID, "request_service_decision", "decision", "skipped", "reason", "max_transfer_limit_reached", "store_ok", true)
			break
		}

		object, found, err := h.loadTransferObjectByItemID(requestedID)
		if err != nil {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, requestedID, "request_service_decision", "decision", "skipped", "reason", "lookup_failed", "store_ok", false, "err", err)
			return err
		}
		if !found {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, requestedID, "request_service_decision", "decision", "missing", "reason", "nil_message", "store_ok", true)
			continue
		}
		if object.Envelope.To != session.queueRecipient {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, requestedID, "request_service_decision", "decision", "skipped", "reason", "recipient_mismatch", "store_ok", true)
			continue
		}
		if object.ExpiryUnixMS <= uint64(now.UnixMilli()) {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, requestedID, "request_service_decision", "decision", "skipped", "reason", "expired", "store_ok", true)
			continue
		}

		objects = append(objects, object)
		expectedReceiptIDs = append(expectedReceiptIDs, object.ItemID)
		debug.LogItem("out", gossipv1.FrameTypeTransfer, object.ItemID, "request_service_decision", "decision", "sent", "reason", "found", "store_ok", true)
	}
	transferCount, transferSample, transferHash := gossipv1.ItemIDSample(expectedReceiptIDs, wsDebugItemSampleLimit)
	debug.Log("out", gossipv1.FrameTypeTransfer, "request_serviced", "object_count", len(objects), "expected_receipt_count", transferCount, "item_ids_sample", transferSample, "item_ids_hash", transferHash, "store_ok", true)

	session.adapter.SetExpectedReceipt(expectedReceiptIDs)
	return h.sendEnvelope(session, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: objects})
}

func (h *WSHandler) handleTransfer(session *clientSession, event gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)
	traceCtx := gossipv1.WithDebugTrace(context.Background(), session.sessionID, session.peerLabel)

	if event.Transfer == nil {
		return fmt.Errorf("gossipv1: transfer event missing payload")
	}

	receipt := gossipv1.ReceiptPayload{
		Accepted: make([]string, 0, len(event.Transfer.Objects)),
	}

	for _, rejected := range event.Transfer.Rejected {
		fields := []any{"object_index", rejected.Index, "item_id", rejected.ID, "reason", rejected.Reason, "detail", rejected.Detail}
		fields = append(fields, gossipv1.TransferObjectDiagnosticLogKV(rejected.Diagnostic)...)
		fields = append(fields, "store_ok", false)
		debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", fields...)
	}
	rejectedIDs := make([]string, 0, len(event.Transfer.Rejected))
	for _, rejected := range event.Transfer.Rejected {
		rejectedIDs = append(rejectedIDs, rejected.ID)
	}

	for _, indexed := range event.Transfer.Objects {
		msg, err := transferObjectToMessage(indexed.Object)
		if err != nil {
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "reason", "policy_reject", "detail", err.Error(), "store_ok", false)
			rejectedIDs = append(rejectedIDs, indexed.Object.ItemID)
			continue
		}

		existing, err := h.store.GetMessageByID(context.Background(), indexed.Object.ItemID)
		if err != nil && !isNotFoundError(err) {
			debug.LogItem("in", gossipv1.FrameTypeTransfer, indexed.Object.ItemID, "transfer_object_store_lookup_failed", "store_ok", false, "err", err)
			return err
		}
		if err == nil && existing != nil {
			if err := h.persistTransferEnvelope(indexed.Object, msg, int(indexed.Object.HopCount), ""); err != nil {
				debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "reason", "ingest_failed", "detail", "envelope persist failed", "store_ok", false, "err", err)
				rejectedIDs = append(rejectedIDs, indexed.Object.ItemID)
				continue
			}
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_accepted", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "store_ok", true)
			receipt.Accepted = append(receipt.Accepted, indexed.Object.ItemID)
			continue
		}

		if err := h.engine.PersistMessage(traceCtx, msg); err != nil {
			metrics.IncrementStoreErrors()
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "reason", "ingest_failed", "detail", "persist failed", "store_ok", false, "err", err)
			rejectedIDs = append(rejectedIDs, indexed.Object.ItemID)
			continue
		}

		metrics.IncrementPersisted()
		if err := h.persistTransferEnvelope(indexed.Object, msg, int(indexed.Object.HopCount), ""); err != nil {
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "reason", "ingest_failed", "detail", "envelope persist failed", "store_ok", false, "err", err)
			rejectedIDs = append(rejectedIDs, indexed.Object.ItemID)
			continue
		}
		debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_accepted", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "store_ok", true)
		receipt.Accepted = append(receipt.Accepted, indexed.Object.ItemID)
	}

	if len(receipt.Accepted) > 1 {
		gossipv1.SortDigestHexIDs(receipt.Accepted)
	}
	acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(receipt.Accepted, wsDebugItemSampleLimit)
	rejectedCount, rejectedSample, rejectedHash := gossipv1.ItemIDSample(rejectedIDs, wsDebugItemSampleLimit)
	debug.Log("out", gossipv1.FrameTypeReceipt, "receipt_constructed", "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash, "rejected_count", rejectedCount, "rejected_item_ids_sample", rejectedSample, "rejected_item_ids_hash", rejectedHash)

	return h.sendEnvelope(session, gossipv1.FrameTypeReceipt, receipt)
}

func (h *WSHandler) handleReceipt(session *clientSession, event gossipv1.Event) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

	if event.Receipt == nil {
		return fmt.Errorf("gossipv1: receipt event missing payload")
	}

	for _, acceptedID := range event.Receipt.Accepted {
		transitioned, err := h.engine.AckClientDelivery(context.Background(), acceptedID, session.client.DeliveryID)
		if err != nil {
			metrics.IncrementStoreErrors()
			debug.LogItem("in", gossipv1.FrameTypeReceipt, acceptedID, "receipt_ack_failed", "store_ok", false, "err", err)
			return err
		}
		if transitioned {
			metrics.IncrementDelivered()
			debug.LogItem("in", gossipv1.FrameTypeReceipt, acceptedID, "receipt_ack_applied", "store_ok", true)
			continue
		}
		debug.LogItem("in", gossipv1.FrameTypeReceipt, acceptedID, "receipt_ack_noop", "store_ok", true)
	}
	acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(event.Receipt.Accepted, wsDebugItemSampleLimit)
	debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_processed", "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash)

	return nil
}

func (h *WSHandler) computeMissingWant(summary *gossipv1.SummaryPayload, candidateIDs []string, peerMaxWant uint64, debugLoggers ...gossipv1.DebugSessionLogger) ([]string, error) {
	if summary == nil {
		return []string{}, nil
	}

	debugEnabled := gossipv1.DebugLoggingEnabled() && len(debugLoggers) > 0
	var debug gossipv1.DebugSessionLogger
	if debugEnabled {
		debug = debugLoggers[0]
	}
	logDecision := func(source string, itemID string, reason string) {
		if !debugEnabled {
			return
		}
		debug.LogItem("local", gossipv1.FrameTypeRequest, itemID, "want_decision", "source", source, "reason", reason)
	}

	previewIDs, err := gossipv1.NormalizeDigestHexIDs(summary.PreviewItemIDs)
	if err != nil {
		return nil, err
	}
	bloom := summary.BloomFilter

	limit := int(gossipv1.MaxWantItems)
	if peerMaxWant > 0 && peerMaxWant < uint64(limit) {
		limit = int(peerMaxWant)
	}
	if limit <= 0 {
		return []string{}, nil
	}

	want := make([]string, 0, limit)
	seenWant := make(map[string]struct{}, limit)
	for _, id := range previewIDs {
		if _, duplicate := seenWant[id]; duplicate {
			logDecision("preview", id, "duplicate_selected")
			continue
		}
		if !gossipv1.BloomFilterMightContain(bloom, id) {
			logDecision("preview", id, "bloom_miss")
			continue
		}

		known, knownErr := h.hasObjectID(id)
		if knownErr != nil {
			logDecision("preview", id, "known_lookup_error")
			return nil, knownErr
		}
		if known {
			logDecision("preview", id, "already_have")
			continue
		}
		logDecision("preview", id, "selected")

		seenWant[id] = struct{}{}
		want = append(want, id)
		if len(want) >= limit {
			logDecision("preview", id, "limit_reached")
			gossipv1.SortDigestHexIDs(want)
			return want, nil
		}
	}

	normalizedCandidates, err := gossipv1.NormalizeDigestHexIDs(candidateIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range normalizedCandidates {
		if _, duplicate := seenWant[id]; duplicate {
			logDecision("candidate", id, "duplicate_selected")
			continue
		}
		if !gossipv1.BloomFilterMightContain(bloom, id) {
			logDecision("candidate", id, "bloom_miss")
			continue
		}

		known, knownErr := h.hasObjectID(id)
		if knownErr != nil {
			logDecision("candidate", id, "known_lookup_error")
			return nil, knownErr
		}
		if known {
			logDecision("candidate", id, "already_have")
			continue
		}
		logDecision("candidate", id, "selected")

		seenWant[id] = struct{}{}
		want = append(want, id)
		if len(want) >= limit {
			logDecision("candidate", id, "limit_reached")
			break
		}
	}

	gossipv1.SortDigestHexIDs(want)
	return want, nil
}

func (h *WSHandler) hasObjectID(id string) (bool, error) {
	if h.envelopeStore != nil {
		envelope, err := h.envelopeStore.GetEnvelopeByID(context.Background(), id)
		if err != nil {
			if !isNotFoundError(err) {
				return false, err
			}
		} else if envelope != nil {
			return true, nil
		}
	}

	message, err := h.store.GetMessageByID(context.Background(), id)
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return message != nil, nil
}

func validateSummaryPreviewCursor(previewIDs []string, previewCursor string) error {
	if len(previewIDs) == 0 {
		if previewCursor != "" {
			return fmt.Errorf("gossipv1: summary preview_cursor must be absent when preview_item_ids is empty")
		}
		return nil
	}

	if previewCursor == "" {
		return fmt.Errorf("gossipv1: summary preview_cursor is required when preview_item_ids are present")
	}
	if previewCursor != previewIDs[len(previewIDs)-1] {
		return fmt.Errorf("gossipv1: summary preview_cursor must equal last preview_item_ids element")
	}
	return nil
}

func (h *WSHandler) persistTransferEnvelope(object gossipv1.TransferObject, msg *model.Message, hopCount int, originRelayID string) error {
	if h.envelopeStore == nil || msg == nil {
		return nil
	}
	envelopeBytes, err := base64.RawURLEncoding.Strict().DecodeString(object.EnvelopeB64)
	if err != nil {
		return fmt.Errorf("decode envelope_b64: %w", err)
	}
	return h.envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:              msg.ID,
		DestinationID:   msg.To,
		OpaquePayload:   envelopeBytes,
		OriginRelayID:   originRelayID,
		CurrentHopCount: hopCount,
		CreatedAt:       msg.CreatedAt,
		ExpiresAt:       msg.ExpiresAt,
	})
}

func transferObjectToMessage(object gossipv1.TransferObject) (*model.Message, error) {
	if object.ItemID == "" {
		return nil, fmt.Errorf("item_id is required")
	}
	if !gossipv1.IsDigestHexID(object.ItemID) {
		return nil, fmt.Errorf("item_id must be 64 lowercase hex chars")
	}
	envelope, err := gossipv1.DecodeItemEnvelopeB64(object.EnvelopeB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope_b64: %w", err)
	}
	if envelope.To == "" {
		return nil, fmt.Errorf("envelope destination is required")
	}
	if envelope.From == "" {
		return nil, fmt.Errorf("envelope manifest/source is required")
	}
	if _, err := model.DecodePayloadB64(envelope.PayloadB64); err != nil {
		return nil, fmt.Errorf("invalid payload_b64")
	}
	if envelope.ExpiresAt > 0 && object.ExpiryUnixMS != uint64(envelope.ExpiresAt)*1000 {
		return nil, fmt.Errorf("expiry_unix_ms must equal envelope expiry")
	}
	if object.HopCount > 65535 {
		return nil, fmt.Errorf("hop_count must be <= 65535")
	}
	if object.ItemID != gossipv1.ComputeTransferObjectItemID(object) {
		return nil, fmt.Errorf("item_id must equal sha256(envelope bytes)")
	}
	now := time.Now().UTC()
	createdAt := now
	if envelope.CreatedAt > 0 {
		createdAt = time.Unix(envelope.CreatedAt, 0).UTC()
	}
	expiresAt := time.UnixMilli(int64(object.ExpiryUnixMS)).UTC()
	if envelope.ExpiresAt > 0 {
		expiresAt = time.Unix(envelope.ExpiresAt, 0).UTC()
	}
	if !expiresAt.After(createdAt) {
		if !expiresAt.After(now) {
			return nil, fmt.Errorf("object already expired")
		}
		createdAt = expiresAt.Add(-time.Second)
	}
	return &model.Message{ID: object.ItemID, From: envelope.From, To: envelope.To, Payload: envelope.PayloadB64, CreatedAt: createdAt, ExpiresAt: expiresAt}, nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// writePump writes messages to the WebSocket.
func (h *WSHandler) writePump(session *clientSession) {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)
	debug.Log("local", "SESSION", "session_state", "state", "write_pump_started")

	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		session.client.Conn.Close()
		debug.Log("local", "SESSION", "session_state", "state", "write_pump_stopped")
	}()

	for {
		select {
		case message, ok := <-session.client.Send:
			if !ok {
				if err := session.client.Conn.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
					debug.Log("out", "CLOSE", "close_frame_failed", "transport_ok", false, "err", err)
				} else {
					debug.Log("out", "CLOSE", "close_frame_sent", "transport_ok", true)
				}
				return
			}
			frameType := gossipv1.FrameTypeFromPrefixedBinaryFrame(message)
			if err := session.client.Conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				debug.Log("out", frameType, "send_failed", "bytes_out", len(message), "transport_ok", false, "protocol_ok", true, "err", err)
				return
			}
			debug.Log("out", frameType, "sent_transport", "bytes_out", len(message), "transport_ok", true, "protocol_ok", true)
		case <-ticker.C:
			if err := session.client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				debug.Log("out", "PING", "heartbeat_failed", "transport_ok", false, "err", err)
				return
			}
			debug.Log("out", "PING", "heartbeat_sent", "bytes_out", 0, "transport_ok", true)
		}
	}
}

func summaryPreviewFromPayload(payload any) []string {
	switch typed := payload.(type) {
	case map[string]any:
		raw, ok := typed["preview_item_ids"]
		if !ok {
			return nil
		}
		rawIDs, ok := raw.([]string)
		if ok {
			return append([]string(nil), rawIDs...)
		}
		asAny, ok := raw.([]any)
		if !ok {
			return nil
		}
		ids := make([]string, 0, len(asAny))
		for _, value := range asAny {
			id, ok := value.(string)
			if !ok {
				continue
			}
			ids = append(ids, id)
		}
		return ids
	default:
		return nil
	}
}

func (h *WSHandler) buildRecipientSummary(queueRecipient string, afterCursor string) (gossipv1.SummaryPayload, string, error) {
	if queueRecipient == "" {
		return gossipv1.BuildSummaryPreviewPayload(nil, nil, ""), "", nil
	}
	if h.envelopeStore == nil {
		return gossipv1.BuildSummaryPreviewPayload(nil, nil, ""), "", nil
	}

	previewIDs, nextCursor, totalCount, eligibleIDs, err := h.envelopeStore.GetEnvelopeIDsByDestinationPage(context.Background(), queueRecipient, afterCursor, int(gossipv1.MaxSummaryPreviewItems))
	if err != nil {
		return gossipv1.SummaryPayload{}, "", err
	}

	normalizedPreview, err := gossipv1.NormalizeDigestHexIDs(previewIDs)
	if err != nil {
		return gossipv1.SummaryPayload{}, "", err
	}
	if uint64(len(normalizedPreview)) > gossipv1.MaxSummaryPreviewItems {
		normalizedPreview = normalizedPreview[:gossipv1.MaxSummaryPreviewItems]
	}

	summary := gossipv1.BuildSummaryPreviewPayload(eligibleIDs, normalizedPreview, "")
	summary.ItemCount = uint64(totalCount)
	return summary, nextCursor, nil
}

func canonicalTransferObjectFromMessage(message *model.Message) (gossipv1.TransferObject, bool) {
	_ = message
	return gossipv1.TransferObject{}, false
}

func (h *WSHandler) loadTransferObjectByItemID(itemID string) (gossipv1.TransferObject, bool, error) {
	if !gossipv1.IsDigestHexID(itemID) {
		return gossipv1.TransferObject{}, false, nil
	}
	if h.envelopeStore != nil {
		env, err := h.envelopeStore.GetEnvelopeByID(context.Background(), itemID)
		if err != nil {
			if !isNotFoundError(err) {
				return gossipv1.TransferObject{}, false, err
			}
		} else if env != nil && len(env.OpaquePayload) > 0 {
			envelopeB64 := base64.RawURLEncoding.EncodeToString(env.OpaquePayload)
			object := gossipv1.TransferObject{
				ItemID:       itemID,
				EnvelopeB64:  envelopeB64,
				ExpiryUnixMS: uint64(env.ExpiresAt.UTC().UnixMilli()),
				HopCount:     uint64(env.CurrentHopCount),
			}
			if object.ItemID != gossipv1.ComputeTransferObjectItemID(object) {
				return gossipv1.TransferObject{}, false, nil
			}
			parsedEnvelope, err := gossipv1.DecodeItemEnvelopeB64(envelopeB64)
			if err != nil {
				return gossipv1.TransferObject{}, false, nil
			}
			object.Envelope = parsedEnvelope
			return object, true, nil
		}
	}

	message, err := h.store.GetMessageByID(context.Background(), itemID)
	if err != nil {
		if isNotFoundError(err) {
			return gossipv1.TransferObject{}, false, nil
		}
		return gossipv1.TransferObject{}, false, err
	}
	if message == nil {
		return gossipv1.TransferObject{}, false, nil
	}

	object, ok := canonicalTransferObjectFromMessage(message)
	if !ok {
		return gossipv1.TransferObject{}, false, nil
	}

	return object, true, nil
}

func (h *WSHandler) sendLocalHello(session *clientSession, adapter *gossipv1.SessionAdapter) error {
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, session.peerLabel)

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		debug.Log("out", gossipv1.FrameTypeHello, "encode_failed", "transport_ok", false, "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	queued := h.sendBinary(session.client, helloBytes)
	debug.Log("out", gossipv1.FrameTypeHello, "sent", "bytes_out", len(helloBytes), "transport_ok", queued, "protocol_ok", true, "store_ok", true)
	return nil
}

func (h *WSHandler) sendEnvelope(session *clientSession, frameType string, payload any) error {
	peerLabel := session.peerLabel
	if peerLabel == "" {
		peerLabel = "-"
	}
	debug := gossipv1.NewDebugSessionLogger(session.sessionID, peerLabel)

	frame, err := gossipv1.EncodeEnvelope(frameType, payload)
	if err != nil {
		debug.Log("out", frameType, "encode_failed", "transport_ok", false, "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		debug.Log("out", frameType, "prefix_failed", "transport_ok", false, "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}

	metadata := make([]any, 0, 12)
	switch frameType {
	case gossipv1.FrameTypeSummary:
		if summary, ok := payload.(gossipv1.SummaryPayload); ok {
			count, sample, hash := gossipv1.ItemIDSample(summary.PreviewItemIDs, wsDebugItemSampleLimit)
			metadata = append(metadata,
				"item_count", summary.ItemCount,
				"bloom_bytes", len(summary.BloomFilter),
				"preview_count", count,
				"item_ids_sample", sample,
				"item_ids_hash", hash,
				"preview_cursor", summary.PreviewCursor,
			)
		} else {
			have := summaryPreviewFromPayload(payload)
			count, sample, hash := gossipv1.ItemIDSample(have, wsDebugItemSampleLimit)
			metadata = append(metadata,
				"item_count", count,
				"preview_count", count,
				"item_ids_sample", sample,
				"item_ids_hash", hash,
			)
		}
	case gossipv1.FrameTypeRequest:
		if request, ok := payload.(gossipv1.RequestPayload); ok {
			count, sample, hash := gossipv1.ItemIDSample(request.Want, wsDebugItemSampleLimit)
			metadata = append(metadata,
				"requested_count", count,
				"item_ids_sample", sample,
				"item_ids_hash", hash,
			)
		}
	case gossipv1.FrameTypeTransfer:
		if transfer, ok := payload.(gossipv1.TransferPayload); ok {
			ids := make([]string, 0, len(transfer.Objects))
			for _, object := range transfer.Objects {
				ids = append(ids, object.ItemID)
			}
			count, sample, hash := gossipv1.ItemIDSample(ids, wsDebugItemSampleLimit)
			metadata = append(metadata,
				"object_count", count,
				"accepted_item_ids_sample", sample,
				"accepted_item_ids_hash", hash,
			)
		}
	case gossipv1.FrameTypeReceipt:
		if receipt, ok := payload.(gossipv1.ReceiptPayload); ok {
			acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(receipt.Accepted, wsDebugItemSampleLimit)
			metadata = append(metadata,
				"item_count", acceptedCount,
				"accepted_count", acceptedCount,
				"accepted_item_ids_sample", acceptedSample,
				"accepted_item_ids_hash", acceptedHash,
			)
		}
	}

	queued := h.sendBinary(session.client, prefixed)
	logFields := make([]any, 0, len(metadata)+6)
	logFields = append(logFields, metadata...)
	logFields = append(logFields, "bytes_out", len(prefixed), "transport_ok", queued, "protocol_ok", true, "store_ok", true)
	debug.Log("out", frameType, "sent", logFields...)
	return nil
}

// sendBinary sends a frame to the client with bounded backpressure.
func (h *WSHandler) sendBinary(client *model.Client, data []byte) bool {
	queued := false
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ws: failed to send, channel closed for client %s", client.WayfarerID)
			metrics.IncrementDropped()
			queued = false
		}
	}()

	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()

	select {
	case client.Send <- data:
		queued = true
	case <-timer.C:
		log.Printf("ws: failed to send, channel full after timeout for client %s", client.WayfarerID)
		metrics.IncrementDropped()
	}

	return queued
}
