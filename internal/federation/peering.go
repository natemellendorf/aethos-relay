package federation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

const (
	HeartbeatInterval = 30 * time.Second

	ReconnectBaseDelay = 1 * time.Second
	ReconnectMaxDelay  = 60 * time.Second

	ProtocolDiagnosticsInterval = 10 * time.Second

	relayForwardMaxPayloadBytes = model.MAX_ENVELOPE_SIZE
)

type Peer struct {
	ID             string
	URL            string
	RemoteNodeID   string
	Outbound       bool
	Conn           *websocket.Conn
	writeMu        sync.Mutex
	localHelloSent bool
	summarySent    bool
	ConnectedAt    time.Time
	LastHelloAt    time.Time
	Health         PeerHealth
	healthMu       sync.Mutex
	Done           chan struct{}
	doneOnce       sync.Once
	adapter        *gossipv1.SessionAdapter
	adapterMu      sync.Mutex
	LastMalformed  time.Time
	Metrics        *model.PeerMetrics
}

type PeerHealth struct {
	Uptime            time.Duration
	LastSeen          time.Time
	MessagesForwarded int
	MessagesReceived  int
	FailureCount      int
	BackoffUntil      time.Time
	IsHealthy         bool
}

func (h *PeerHealth) RecordFailure() {
	h.FailureCount++
	backoff := time.Duration(min(h.FailureCount*h.FailureCount, 300)) * time.Second
	h.BackoffUntil = time.Now().Add(backoff)
	h.IsHealthy = false
}

func (h *PeerHealth) RecordSuccess() {
	h.FailureCount = 0
	h.BackoffUntil = time.Time{}
	h.IsHealthy = true
}

func (h *PeerHealth) IsBackingOff() bool {
	return time.Now().Before(h.BackoffUntil)
}

type PeerManager struct {
	relayID string
	store   store.Store
	clients *model.ClientRegistry
	maxTTL  time.Duration

	ctxStop chan struct{}

	peers   map[string]*Peer
	peersMu sync.RWMutex

	outbound chan string
	inbound  chan *Peer

	tarConfig        *TARConfig
	forwardingConfig *ForwardingConfig
	envelopeStore    store.EnvelopeStore
	engine           *storeforward.Engine

	peerMetrics map[string]*model.PeerMetrics
	metricsMu   sync.RWMutex

	eventObserverMu sync.RWMutex
	eventObserver   func(*Peer, gossipv1.Event) error
}

func NewPeerManager(relayID string, st store.Store, clients *model.ClientRegistry, maxTTL time.Duration) *PeerManager {
	return NewPeerManagerWithConfig(relayID, st, clients, maxTTL, DefaultTARConfig(), DefaultForwardingConfig())
}

func NewPeerManagerWithConfig(relayID string, st store.Store, clients *model.ClientRegistry, maxTTL time.Duration, tarConfig *TARConfig, forwardingConfig *ForwardingConfig) *PeerManager {
	if err := tarConfig.Validate(); err != nil {
		panic(err)
	}
	if err := forwardingConfig.Validate(); err != nil {
		panic(err)
	}

	var engine *storeforward.Engine
	if st != nil {
		engine = storeforward.New(st, maxTTL)
		engine.ConfigureFederation(relayID, nil)
	}

	pm := &PeerManager{
		relayID:          relayID,
		store:            st,
		clients:          clients,
		maxTTL:           maxTTL,
		ctxStop:          make(chan struct{}),
		peers:            make(map[string]*Peer),
		outbound:         make(chan string, 100),
		inbound:          make(chan *Peer, 100),
		tarConfig:        tarConfig,
		forwardingConfig: forwardingConfig,
		engine:           engine,
		peerMetrics:      make(map[string]*model.PeerMetrics),
	}

	if pm.engine != nil {
		pm.engine.SetRelayIngestObserver(func(_ context.Context, signal storeforward.RelayIngestSignal) {
			if signal.ItemID == "" {
				return
			}
			if err := pm.forwardSummaryToPeers([]string{signal.ItemID}, signal.SourceRelay); err != nil {
				log.Printf("federation: failed propagating relay ingest summary item=%s err=%v", signal.ItemID, err)
			}
		})
	}

	return pm
}

func (pm *PeerManager) SetEnvelopeStore(envelopeStore store.EnvelopeStore) {
	pm.envelopeStore = envelopeStore
	if pm.engine == nil {
		return
	}
	pm.engine.ConfigureFederation(pm.relayID, envelopeStore)
}

func (pm *PeerManager) SetAckDrivenSuppression(enabled bool) {
	if pm.engine == nil {
		return
	}
	pm.engine.SetAckDrivenSuppression(enabled)
}

func (pm *PeerManager) SetEventObserver(observer func(*Peer, gossipv1.Event) error) {
	pm.eventObserverMu.Lock()
	defer pm.eventObserverMu.Unlock()
	pm.eventObserver = observer
}

func (pm *PeerManager) AddPeerURL(url string) {
	select {
	case pm.outbound <- url:
	case <-pm.ctxStop:
	}
}

func (pm *PeerManager) Run() {
	for {
		select {
		case <-pm.ctxStop:
			pm.cleanup()
			return
		case peer := <-pm.inbound:
			pm.addPeer(peer)
		case url := <-pm.outbound:
			go pm.dialPeer(url)
		}
	}
}

func (pm *PeerManager) Stop() {
	select {
	case <-pm.ctxStop:
		return
	default:
		close(pm.ctxStop)
	}
}

func (pm *PeerManager) cleanup() {
	pm.peersMu.Lock()
	defer pm.peersMu.Unlock()
	for _, peer := range pm.peers {
		peer.doneOnce.Do(func() { close(peer.Done) })
		_ = peer.Conn.Close()
	}
}

func (pm *PeerManager) dialPeer(url string) {
	delay := ReconnectBaseDelay

	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-pm.ctxStop:
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Printf("federation: dial failed %s: %v (retry in %v)", url, err, delay)
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}
		conn.SetReadLimit(model.WebSocketReadLimitBytes)

		peer := &Peer{
			ID:          uuid.New().String(),
			URL:         url,
			Outbound:    true,
			Conn:        conn,
			ConnectedAt: time.Now(),
			Health:      PeerHealth{LastSeen: time.Now()},
			Done:        make(chan struct{}),
			Metrics:     model.NewPeerMetrics(url),
		}

		if err := pm.runEncounter(peer); err != nil {
			log.Printf("federation: gossipv1 encounter failed %s: %v", url, err)
			_ = conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		pm.inbound <- peer
		return
	}

	log.Printf("federation: max retries exceeded for %s", url)
}

func (pm *PeerManager) runEncounter(peer *Peer) error {
	localHello := gossipv1.BuildRelayHello(pm.relayID)
	adapter := gossipv1.NewSessionAdapter(localHello, true)
	peer.adapterMu.Lock()
	peer.adapter = adapter
	peer.adapterMu.Unlock()

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		return err
	}
	if err := pm.writePeerMessage(peer, websocket.BinaryMessage, helloBytes); err != nil {
		return fmt.Errorf("send HELLO: %w", err)
	}
	peer.localHelloSent = true

	if err := peer.Conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	msgType, data, err := peer.Conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read HELLO: %w", err)
	}
	if msgType != websocket.BinaryMessage {
		return errors.New("expected binary gossip frame")
	}

	events := adapter.PushInbound(data)
	if err := pm.processEvents(peer, adapter, events); err != nil {
		return err
	}
	if !adapter.IsHealthy() {
		return errors.New("gossip session unhealthy after HELLO")
	}

	_ = peer.Conn.SetReadDeadline(time.Time{})
	go pm.readLoop(peer)
	go pm.writeLoop(peer)
	return nil
}

func (pm *PeerManager) processEvents(peer *Peer, adapter *gossipv1.SessionAdapter, events []gossipv1.Event) error {
	for _, event := range events {
		if event.Type == gossipv1.EventTypeFatal {
			return event.Err
		}

		switch event.Type {
		case gossipv1.EventTypeHelloValidated:
			if event.Hello == nil {
				return errors.New("gossipv1: hello event missing payload")
			}
			if peer.RemoteNodeID == "" {
				peer.RemoteNodeID = event.Hello.NodeID
			} else if peer.RemoteNodeID != event.Hello.NodeID {
				return fmt.Errorf("gossipv1: peer node id changed from %s to %s", peer.RemoteNodeID, event.Hello.NodeID)
			}

			peer.healthMu.Lock()
			peer.Health.LastSeen = time.Now()
			peer.Health.IsHealthy = true
			peer.LastHelloAt = time.Now()
			peer.healthMu.Unlock()

			if err := pm.sendSummary(peer); err != nil {
				return err
			}
		case gossipv1.EventTypeSummary:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: summary before hello")
			}
			if err := pm.handleSummary(peer, event); err != nil {
				return err
			}
		case gossipv1.EventTypeRequest:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: request before hello")
			}
			if err := pm.handleRequest(peer, event); err != nil {
				return err
			}
		case gossipv1.EventTypeTransfer:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: transfer before hello")
			}
			if err := pm.handleTransfer(peer, event); err != nil {
				return err
			}
		case gossipv1.EventTypeReceipt:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: receipt before hello")
			}
			if err := pm.handleReceipt(peer, event); err != nil {
				return err
			}
		case gossipv1.EventTypeRelayIngest, gossipv1.EventTypeUntrustedRelay:
			// Phase 2 parses relay_ingest frames and surfaces them for observation,
			// but does not attach trusted relay-side effects in peer manager yet.
		case gossipv1.EventTypeIgnored:
			// Intentionally ignored.
		}

		pm.eventObserverMu.RLock()
		observer := pm.eventObserver
		pm.eventObserverMu.RUnlock()
		if observer == nil {
			continue
		}

		if err := observer(peer, event); err != nil {
			adapter.ObserveNonFatal(err)
			log.Printf("federation: non-fatal observer error peer=%s type=%s err=%v", peer.ID, event.Type, err)
		}
	}

	return nil
}

func (pm *PeerManager) readLoop(peer *Peer) {
	defer func() {
		pm.removePeer(peer)
		_ = peer.Conn.Close()
	}()

	for {
		msgType, data, err := peer.Conn.ReadMessage()
		if err != nil {
			log.Printf("federation: read error from %s: %v", peer.URL, err)
			return
		}
		if msgType != websocket.BinaryMessage {
			log.Printf("federation: non-binary frame from %s type=%d", peer.URL, msgType)
			return
		}

		peer.adapterMu.Lock()
		adapter := peer.adapter
		peer.adapterMu.Unlock()
		if adapter == nil {
			log.Printf("federation: missing adapter for peer %s", peer.ID)
			return
		}

		events := adapter.PushInbound(data)
		if err := pm.processEvents(peer, adapter, events); err != nil {
			log.Printf("federation: fatal validation error peer=%s err=%v", peer.ID, err)
			return
		}
	}
}

func (pm *PeerManager) writeLoop(peer *Peer) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-peer.Done:
			return
		case <-ticker.C:
			if err := pm.writePeerMessage(peer, websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (pm *PeerManager) addPeer(peer *Peer) {
	pm.peersMu.Lock()
	pm.peers[peer.ID] = peer
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	if peer.Metrics == nil {
		peer.Metrics = model.NewPeerMetrics(peer.ID)
	}
	pm.peerMetrics[peer.ID] = peer.Metrics
	pm.metricsMu.Unlock()
}

func (pm *PeerManager) removePeer(peer *Peer) {
	if peer == nil {
		return
	}

	peer.doneOnce.Do(func() { close(peer.Done) })

	pm.peersMu.Lock()
	delete(pm.peers, peer.ID)
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	delete(pm.peerMetrics, peer.ID)
	pm.metricsMu.Unlock()

	if peer.Outbound {
		select {
		case <-pm.ctxStop:
		case pm.outbound <- peer.URL:
		}
	}
}

func (pm *PeerManager) HandleInboundPeer(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("federation: upgrade failed: %v", err)
		return
	}
	conn.SetReadLimit(model.WebSocketReadLimitBytes)

	peer := &Peer{
		ID:          uuid.New().String(),
		URL:         r.RemoteAddr,
		Conn:        conn,
		ConnectedAt: time.Now(),
		Health:      PeerHealth{LastSeen: time.Now()},
		Done:        make(chan struct{}),
		Metrics:     model.NewPeerMetrics(r.RemoteAddr),
	}

	if err := pm.acceptInboundEncounter(peer); err != nil {
		log.Printf("federation: inbound encounter failed from %s: %v", r.RemoteAddr, err)
		_ = conn.Close()
		return
	}

	pm.inbound <- peer
}

func (pm *PeerManager) acceptInboundEncounter(peer *Peer) error {
	localHello := gossipv1.BuildRelayHello(pm.relayID)
	adapter := gossipv1.NewSessionAdapter(localHello, true)

	if err := peer.Conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	msgType, data, err := peer.Conn.ReadMessage()
	if err != nil {
		return err
	}
	if msgType != websocket.BinaryMessage {
		return errors.New("expected binary gossip frame")
	}

	events := adapter.PushInbound(data)
	if err := firstFatal(events); err != nil {
		return err
	}

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		return err
	}
	if err := pm.writePeerMessage(peer, websocket.BinaryMessage, helloBytes); err != nil {
		return err
	}
	peer.localHelloSent = true

	if err := pm.processEvents(peer, adapter, events); err != nil {
		return err
	}
	if !adapter.IsHealthy() {
		return errors.New("gossip session unhealthy after inbound HELLO")
	}

	peer.adapterMu.Lock()
	peer.adapter = adapter
	peer.adapterMu.Unlock()

	_ = peer.Conn.SetReadDeadline(time.Time{})
	go pm.readLoop(peer)
	go pm.writeLoop(peer)
	return nil
}

func (pm *PeerManager) PeerLastObserverError(peerID string) error {
	pm.peersMu.RLock()
	peer, ok := pm.peers[peerID]
	pm.peersMu.RUnlock()
	if !ok || peer == nil {
		return nil
	}

	peer.adapterMu.Lock()
	adapter := peer.adapter
	peer.adapterMu.Unlock()
	if adapter == nil {
		return nil
	}

	return adapter.LastObserverError()
}

func (pm *PeerManager) AnnounceMessage(msg *model.Message) error {
	if msg == nil {
		return errors.New("federation: announce message is required")
	}
	return pm.forwardSummaryToPeers([]string{msg.ID}, "")
}

func (pm *PeerManager) ForwardToPeers(msg *model.Message, originNodeID string) error {
	if msg == nil {
		return errors.New("federation: forward message is required")
	}
	return pm.forwardSummaryToPeers([]string{msg.ID}, originNodeID)
}

func firstFatal(events []gossipv1.Event) error {
	for _, event := range events {
		if event.Type == gossipv1.EventTypeFatal {
			return event.Err
		}
	}
	return nil
}

func (pm *PeerManager) writePeerMessage(peer *Peer, messageType int, payload []byte) error {
	if peer == nil || peer.Conn == nil {
		return errors.New("federation: peer connection is not available")
	}

	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	return peer.Conn.WriteMessage(messageType, payload)
}

func (pm *PeerManager) sendEnvelope(peer *Peer, frameType string, payload any) error {
	frame, err := gossipv1.EncodeEnvelope(frameType, payload)
	if err != nil {
		return err
	}
	prefixed, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		return err
	}
	return pm.writePeerMessage(peer, websocket.BinaryMessage, prefixed)
}

func (pm *PeerManager) sendSummary(peer *Peer) error {
	if peer == nil {
		return errors.New("federation: peer is required for summary")
	}
	if peer.Conn == nil {
		return nil
	}
	if !peer.localHelloSent {
		return nil
	}
	if peer.summarySent {
		return nil
	}

	have, err := pm.listSummaryHaveIDs(context.Background())
	if err != nil {
		return err
	}
	if err := pm.sendEnvelope(peer, gossipv1.FrameTypeSummary, map[string]any{"have": have}); err != nil {
		return err
	}
	peer.summarySent = true
	return nil
}

func (pm *PeerManager) handleSummary(peer *Peer, event gossipv1.Event) error {
	if event.Summary == nil {
		return errors.New("gossipv1: summary event missing payload")
	}
	want, err := pm.computeMissingWant(context.Background(), event.Summary.Have)
	if err != nil {
		return err
	}
	if len(want) == 0 {
		return nil
	}
	return pm.sendEnvelope(peer, gossipv1.FrameTypeRequest, gossipv1.RequestPayload{Want: want})
}

func (pm *PeerManager) handleRequest(peer *Peer, event gossipv1.Event) error {
	if event.Request == nil {
		return errors.New("gossipv1: request event missing payload")
	}
	if len(event.Request.Want) == 0 {
		return nil
	}

	objects, expectedReceiptIDs, err := pm.buildTransferObjects(context.Background(), event.Request.Want)
	if err != nil {
		return err
	}

	peer.adapterMu.Lock()
	if peer.adapter != nil {
		peer.adapter.SetExpectedReceipt(expectedReceiptIDs)
	}
	peer.adapterMu.Unlock()

	return pm.sendEnvelope(peer, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: objects})
}

func (pm *PeerManager) handleTransfer(peer *Peer, event gossipv1.Event) error {
	if event.Transfer == nil {
		return errors.New("gossipv1: transfer event missing payload")
	}
	if pm.engine == nil {
		return errors.New("federation: relay forward engine is not configured")
	}

	receipt := gossipv1.ReceiptPayload{
		Accepted: make([]string, 0, len(event.Transfer.Objects)),
		Rejected: append([]gossipv1.TransferObjectRejection(nil), event.Transfer.Rejected...),
	}

	for _, indexed := range event.Transfer.Objects {
		msg, err := transferObjectToMessage(indexed.Object)
		if err != nil {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: err.Error(),
			})
			continue
		}

		result, err := pm.engine.AcceptRelayForward(context.Background(), peer.RemoteNodeID, msg, relayForwardMaxPayloadBytes)
		if err != nil {
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{
				Index:  indexed.Index,
				ID:     indexed.Object.ID,
				Reason: relayForwardErrorReason(err),
			})
			continue
		}

		switch result.Status {
		case storeforward.RelayForwardAccepted, storeforward.RelayForwardDuplicate, storeforward.RelayForwardSeenLoop:
			receipt.Accepted = append(receipt.Accepted, msg.ID)
		case storeforward.RelayForwardExpired:
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{Index: indexed.Index, ID: msg.ID, Reason: "object already expired"})
		case storeforward.RelayForwardTooLarge:
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{Index: indexed.Index, ID: msg.ID, Reason: "payload exceeds relay limit"})
		case storeforward.RelayForwardInvalid:
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{Index: indexed.Index, ID: msg.ID, Reason: "invalid transfer object"})
		default:
			receipt.Rejected = append(receipt.Rejected, gossipv1.TransferObjectRejection{Index: indexed.Index, ID: msg.ID, Reason: "relay forward rejected"})
		}
	}

	if len(receipt.Accepted) > 1 {
		sort.Strings(receipt.Accepted)
	}

	return pm.sendEnvelope(peer, gossipv1.FrameTypeReceipt, receipt)
}

func (pm *PeerManager) handleReceipt(_ *Peer, event gossipv1.Event) error {
	if event.Receipt == nil {
		return errors.New("gossipv1: receipt event missing payload")
	}
	return nil
}

func (pm *PeerManager) listSummaryHaveIDs(ctx context.Context) ([]string, error) {
	if pm.store == nil {
		return []string{}, nil
	}

	recipients, err := pm.store.GetAllRecipientIDs(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	have := make([]string, 0)
	for _, recipientID := range recipients {
		ids, err := pm.store.GetAllQueuedMessageIDs(ctx, recipientID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			have = append(have, id)
		}
	}

	if len(have) > 1 {
		sort.Strings(have)
	}
	if uint64(len(have)) > gossipv1.MaxSummaryItems {
		have = have[:gossipv1.MaxSummaryItems]
	}

	return have, nil
}

func (pm *PeerManager) computeMissingWant(ctx context.Context, have []string) ([]string, error) {
	if pm.store == nil {
		return []string{}, nil
	}
	if len(have) == 0 {
		return []string{}, nil
	}

	want := make([]string, 0, len(have))
	for _, id := range have {
		if id == "" {
			continue
		}
		msg, err := pm.store.GetMessageByID(ctx, id)
		if err != nil {
			if isNotFoundError(err) {
				want = append(want, id)
				continue
			}
			return nil, err
		}
		if msg == nil {
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

func (pm *PeerManager) buildTransferObjects(ctx context.Context, want []string) ([]gossipv1.TransferObject, []string, error) {
	if pm.store == nil {
		return []gossipv1.TransferObject{}, []string{}, nil
	}
	objects := make([]gossipv1.TransferObject, 0, len(want))
	ids := make([]string, 0, len(want))
	now := time.Now().UTC()

	for _, id := range want {
		if uint64(len(objects)) >= gossipv1.MaxTransferItems {
			break
		}
		if id == "" {
			continue
		}

		msg, err := pm.store.GetMessageByID(ctx, id)
		if err != nil {
			if isNotFoundError(err) {
				continue
			}
			return nil, nil, err
		}
		if msg == nil || !msg.ExpiresAt.After(now) {
			continue
		}

		objects = append(objects, gossipv1.TransferObject{
			ID:         msg.ID,
			From:       msg.From,
			To:         msg.To,
			PayloadB64: msg.Payload,
			CreatedAt:  msg.CreatedAt.UTC().Unix(),
			ExpiresAt:  msg.ExpiresAt.UTC().Unix(),
		})
		ids = append(ids, msg.ID)
	}

	return objects, ids, nil
}

func transferObjectToMessage(object gossipv1.TransferObject) (*model.Message, error) {
	if object.ID == "" {
		return nil, errors.New("id is required")
	}
	if object.From == "" {
		return nil, errors.New("from is required")
	}
	if object.To == "" {
		return nil, errors.New("to is required")
	}
	if object.PayloadB64 == "" {
		return nil, errors.New("payload_b64 is required")
	}
	if _, err := model.DecodePayloadB64(object.PayloadB64); err != nil {
		return nil, errors.New("invalid payload_b64")
	}
	if object.CreatedAt <= 0 {
		return nil, errors.New("created_at must be > 0")
	}
	if object.ExpiresAt <= object.CreatedAt {
		return nil, errors.New("expires_at must be greater than created_at")
	}

	createdAt := time.Unix(object.CreatedAt, 0).UTC()
	expiresAt := time.Unix(object.ExpiresAt, 0).UTC()
	if !expiresAt.After(createdAt) {
		return nil, errors.New("expires_at must be greater than created_at")
	}

	return &model.Message{
		ID:        object.ID,
		From:      object.From,
		To:        object.To,
		Payload:   object.PayloadB64,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}, nil
}

func relayForwardErrorReason(err error) string {
	switch {
	case errors.Is(err, storeforward.ErrForwardMessageTooLarge):
		return "payload exceeds relay limit"
	case errors.Is(err, storeforward.ErrForwardMessageInvalid):
		return "invalid transfer object"
	default:
		return "persist failed"
	}
}

func (pm *PeerManager) forwardSummaryToPeers(ids []string, originNodeID string) error {
	if len(ids) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(ids))
	have := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		have = append(have, id)
	}
	if len(have) == 0 {
		return nil
	}
	if len(have) > 1 {
		sort.Strings(have)
	}
	if uint64(len(have)) > gossipv1.MaxSummaryItems {
		have = have[:gossipv1.MaxSummaryItems]
	}

	pm.peersMu.RLock()
	peers := make([]*Peer, 0, len(pm.peers))
	for _, peer := range pm.peers {
		if peer == nil {
			continue
		}
		if originNodeID != "" && peer.RemoteNodeID == originNodeID {
			continue
		}
		peers = append(peers, peer)
	}
	pm.peersMu.RUnlock()

	for _, peer := range peers {
		if err := pm.sendEnvelope(peer, gossipv1.FrameTypeSummary, map[string]any{"have": have}); err != nil {
			log.Printf("federation: failed forwarding summary to peer=%s err=%v", peer.ID, err)
		}
	}

	return nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func (pm *PeerManager) GetPeers() map[string]PeerHealth {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()

	out := make(map[string]PeerHealth, len(pm.peers))
	for id, peer := range pm.peers {
		peer.healthMu.Lock()
		peer.Health.Uptime = time.Since(peer.ConnectedAt)
		health := peer.Health
		peer.healthMu.Unlock()
		out[id] = health
	}
	return out
}

func (pm *PeerManager) GetHealthyPeers() []string {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()

	healthy := make([]string, 0, len(pm.peers))
	for id, peer := range pm.peers {
		peer.healthMu.Lock()
		isHealthy := peer.Health.IsHealthy && !peer.Health.IsBackingOff()
		peer.healthMu.Unlock()
		if isHealthy {
			healthy = append(healthy, id)
		}
	}

	return healthy
}

func (pm *PeerManager) GetPeerCount() int {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()
	return len(pm.peers)
}

func (pm *PeerManager) GetPeerMetrics() []*model.PeerScoreResponse {
	pm.metricsMu.RLock()
	defer pm.metricsMu.RUnlock()

	responses := make([]*model.PeerScoreResponse, 0, len(pm.peerMetrics))
	for _, metrics := range pm.peerMetrics {
		responses = append(responses, metrics.ToResponse())
	}
	return responses
}

func (pm *PeerManager) IsPeerHealthy(peerID string) bool {
	pm.peersMu.RLock()
	peer, ok := pm.peers[peerID]
	pm.peersMu.RUnlock()
	if !ok {
		return false
	}

	peer.healthMu.Lock()
	defer peer.healthMu.Unlock()
	return peer.Health.IsHealthy && !peer.Health.IsBackingOff()
}

type RateLimiter struct {
	mu          sync.Mutex
	requests    map[string][]time.Time
	maxRequests int
	window      time.Duration
}

func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		requests:    make(map[string][]time.Time),
		maxRequests: maxRequests,
		window:      window,
	}
}

func (rl *RateLimiter) Allow(peerID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.window)

	history := rl.requests[peerID]
	valid := make([]time.Time, 0, len(history)+1)
	for _, t := range history {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxRequests {
		rl.requests[peerID] = valid
		return false
	}

	valid = append(valid, now)
	rl.requests[peerID] = valid
	return true
}

func (rl *RateLimiter) Cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.window)

	for peerID, history := range rl.requests {
		valid := make([]time.Time, 0, len(history))
		for _, t := range history {
			if t.After(windowStart) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, peerID)
			continue
		}
		rl.requests[peerID] = valid
	}
}

func diff(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}

	result := make([]string, 0, len(a))
	for _, v := range a {
		if !set[v] {
			result = append(result, v)
		}
	}

	return result
}
