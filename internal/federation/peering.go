package federation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
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
	debugItemSampleLimit        = 4
)

type Peer struct {
	ID             string
	SessionID      string
	URL            string
	RemoteNodeID   string
	RemoteMaxWant  uint64
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

func peerDebugIdentity(peer *Peer) string {
	if peer == nil {
		return "-"
	}
	if peer.RemoteNodeID != "" {
		return peer.RemoteNodeID
	}
	if peer.URL != "" {
		return peer.URL
	}
	if peer.ID != "" {
		return peer.ID
	}
	return "-"
}

func (pm *PeerManager) peerDebugLogger(peer *Peer) gossipv1.DebugSessionLogger {
	if peer == nil {
		return gossipv1.NewDebugSessionLogger("federation-unknown", "-")
	}
	sessionID := peer.SessionID
	if sessionID == "" {
		sessionID = "federation-unknown"
	}
	return gossipv1.NewDebugSessionLogger(sessionID, peerDebugIdentity(peer))
}

func peerSocketAddresses(peer *Peer) (string, string) {
	if peer == nil || peer.Conn == nil {
		return "-", "-"
	}
	netConn := peer.Conn.UnderlyingConn()
	if netConn == nil {
		return "-", "-"
	}
	localAddr := "-"
	remoteAddr := "-"
	if netConn.LocalAddr() != nil {
		localAddr = netConn.LocalAddr().String()
	}
	if netConn.RemoteAddr() != nil {
		remoteAddr = netConn.RemoteAddr().String()
	}
	return localAddr, remoteAddr
}

func closeReasonFromError(err error) string {
	if err == nil {
		return ""
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		return fmt.Sprintf("code=%d text=%s", closeErr.Code, closeErr.Text)
	}
	if errors.Is(err, net.ErrClosed) {
		return "network closed"
	}
	return err.Error()
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
			sampleCount, sampleIDs, sampleHash := gossipv1.ItemIDSample([]string{signal.ItemID}, debugItemSampleLimit)
			gossipv1.NewDebugSessionLogger("relay-ingest-observer", signal.SourceRelay).LogItem(
				"out",
				gossipv1.FrameTypeRelayIngest,
				signal.ItemID,
				"relay_ingest_observed",
				"trusted", signal.Trusted,
				"item_count", sampleCount,
				"item_ids_sample", sampleIDs,
				"item_ids_hash", sampleHash,
			)
			if err := pm.forwardSummaryToPeers([]string{signal.ItemID}, signal.SourceRelay); err != nil {
				log.Printf("federation: failed propagating relay ingest summary item=%s err=%v", signal.ItemID, err)
				gossipv1.NewDebugSessionLogger("relay-ingest-observer", signal.SourceRelay).LogItem(
					"out",
					gossipv1.FrameTypeSummary,
					signal.ItemID,
					"relay_ingest_summary_forward_failed",
					"transport_ok", false,
					"protocol_ok", false,
					"err", err,
				)
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
	sessionID := gossipv1.NewDebugSessionID("federation-out")
	debug := gossipv1.NewDebugSessionLogger(sessionID, url)
	debug.Log("out", "CONNECTION", "session_state", "state", "dialing", "max_attempts", 10)

	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-pm.ctxStop:
			debug.Log("local", "CONNECTION", "session_state", "state", "stopped")
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Printf("federation: dial failed %s: %v (retry in %v)", url, err, delay)
			debug.Log("out", "CONNECTION", "connect_failed", "attempt", attempt+1, "retry_in", delay, "err", err)
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}
		conn.SetReadLimit(model.WebSocketReadLimitBytes)

		peer := &Peer{
			ID:          uuid.New().String(),
			SessionID:   sessionID,
			URL:         url,
			Outbound:    true,
			Conn:        conn,
			ConnectedAt: time.Now(),
			Health:      PeerHealth{LastSeen: time.Now()},
			Done:        make(chan struct{}),
			Metrics:     model.NewPeerMetrics(url),
		}
		localAddr, remoteAddr := peerSocketAddresses(peer)
		debug.Log("out", "CONNECTION", "connect_ok", "attempt", attempt+1, "local_addr", localAddr, "remote_addr", remoteAddr)

		if err := pm.runEncounter(peer); err != nil {
			log.Printf("federation: gossipv1 encounter failed %s: %v", url, err)
			debug.Log("local", "CONNECTION", "session_state", "state", "encounter_failed", "err", err)
			_ = conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		debug.Log("local", "CONNECTION", "session_state", "state", "encounter_established")
		pm.inbound <- peer
		return
	}

	log.Printf("federation: max retries exceeded for %s", url)
	debug.Log("local", "CONNECTION", "session_state", "state", "max_retries_exceeded")
}

func (pm *PeerManager) runEncounter(peer *Peer) error {
	debug := pm.peerDebugLogger(peer)
	debug.Log("local", "SESSION", "session_state", "state", "encounter_start")

	localHello := gossipv1.BuildRelayHello(pm.relayID)
	adapter := gossipv1.NewSessionAdapter(localHello, true)
	peer.adapterMu.Lock()
	peer.adapter = adapter
	peer.adapterMu.Unlock()

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		debug.Log("out", gossipv1.FrameTypeHello, "encode_failed", "transport_ok", false, "protocol_ok", false, "err", err)
		return err
	}
	if err := pm.writePeerMessage(peer, websocket.BinaryMessage, helloBytes); err != nil {
		debug.Log("out", gossipv1.FrameTypeHello, "send_failed", "bytes_out", len(helloBytes), "transport_ok", false, "protocol_ok", false, "err", err)
		return fmt.Errorf("send HELLO: %w", err)
	}
	debug.Log("out", gossipv1.FrameTypeHello, "sent", "bytes_out", len(helloBytes), "transport_ok", true, "protocol_ok", true)
	peer.localHelloSent = true

	if err := peer.Conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		debug.Log("local", "SESSION", "deadline_set_failed", "err", err)
		return err
	}
	msgType, data, err := peer.Conn.ReadMessage()
	if err != nil {
		debug.Log("in", gossipv1.FrameTypeHello, "recv_failed", "transport_ok", false, "protocol_ok", false, "close_reason", closeReasonFromError(err), "err", err)
		return fmt.Errorf("read HELLO: %w", err)
	}
	if msgType != websocket.BinaryMessage {
		debug.Log("in", "NON_BINARY", "recv_rejected", "msg_type_raw", msgType, "bytes_in", len(data), "transport_ok", true, "protocol_ok", false)
		return errors.New("expected binary gossip frame")
	}
	debug.Log("in", gossipv1.FrameTypeFromPrefixedBinaryFrame(data), "received", "bytes_in", len(data), "transport_ok", true)

	events := adapter.PushInbound(data)
	if err := pm.processEvents(peer, adapter, events); err != nil {
		debug.Log("in", adapter.LastFrameType(), "process_failed", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	if !adapter.IsHealthy() {
		debug.Log("local", "SESSION", "session_unhealthy", "state", "post_hello", "protocol_ok", false)
		return errors.New("gossip session unhealthy after HELLO")
	}
	debug.Log("local", "SESSION", "session_state", "state", "healthy")

	_ = peer.Conn.SetReadDeadline(time.Time{})
	go pm.readLoop(peer)
	go pm.writeLoop(peer)
	debug.Log("local", "SESSION", "session_state", "state", "loops_started")
	return nil
}

func (pm *PeerManager) processEvents(peer *Peer, adapter *gossipv1.SessionAdapter, events []gossipv1.Event) error {
	debug := pm.peerDebugLogger(peer)

	for _, event := range events {
		if event.Type == gossipv1.EventTypeFatal {
			debug.Log("in", adapter.LastFrameType(), "protocol_fatal", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", event.Err)
			return event.Err
		}

		switch event.Type {
		case gossipv1.EventTypeHelloValidated:
			if event.Hello == nil {
				return errors.New("gossipv1: hello event missing payload")
			}
			peer.RemoteMaxWant = event.Hello.MaxWant
			if peer.RemoteNodeID == "" {
				peer.RemoteNodeID = event.Hello.NodeID
				debug = pm.peerDebugLogger(peer)
			} else if peer.RemoteNodeID != event.Hello.NodeID {
				return fmt.Errorf("gossipv1: peer node id changed from %s to %s", peer.RemoteNodeID, event.Hello.NodeID)
			}
			debug.Log("in", gossipv1.FrameTypeHello, "hello_received", "transport_ok", true, "protocol_ok", true, "remote_node_id", event.Hello.NodeID)

			peer.healthMu.Lock()
			peer.Health.LastSeen = time.Now()
			peer.Health.IsHealthy = true
			peer.LastHelloAt = time.Now()
			peer.healthMu.Unlock()
			debug.Log("local", "SESSION", "session_state", "state", "hello_validated")

			if err := pm.sendSummary(peer); err != nil {
				debug.Log("out", gossipv1.FrameTypeSummary, "summary_send_failed", "transport_ok", false, "protocol_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeSummary:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: summary before hello")
			}
			if event.Summary != nil {
				count, sample, hash := gossipv1.ItemIDSample(event.Summary.PreviewItemIDs, debugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeSummary, "summary_received", "transport_ok", true, "protocol_ok", true, "item_count", event.Summary.ItemCount, "bloom_bytes", len(event.Summary.BloomFilter), "preview_count", count, "item_ids_sample", sample, "item_ids_hash", hash, "preview_cursor", event.Summary.PreviewCursor)
			}
			if err := pm.handleSummary(peer, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeSummary, "summary_handle_failed", "protocol_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeRequest:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: request before hello")
			}
			if event.Request != nil {
				count, sample, hash := gossipv1.ItemIDSample(event.Request.Want, debugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeRequest, "request_received", "transport_ok", true, "protocol_ok", true, "requested_count", count, "item_ids_sample", sample, "item_ids_hash", hash)
			}
			if err := pm.handleRequest(peer, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeRequest, "request_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeTransfer:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: transfer before hello")
			}
			if event.Transfer != nil {
				transferIDs := make([]string, 0, len(event.Transfer.Objects))
				for _, indexed := range event.Transfer.Objects {
					transferIDs = append(transferIDs, indexed.Object.ItemID)
				}
				count, sample, hash := gossipv1.ItemIDSample(transferIDs, debugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_received", "transport_ok", true, "protocol_ok", true, "object_count", count, "item_ids_sample", sample, "item_ids_hash", hash, "parse_rejected_count", len(event.Transfer.Rejected))
			}
			if err := pm.handleTransfer(peer, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeReceipt:
			if !adapter.IsHealthy() {
				return errors.New("gossipv1: receipt before hello")
			}
			if event.Receipt != nil {
				acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(event.Receipt.Accepted, debugItemSampleLimit)
				debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_received", "transport_ok", true, "protocol_ok", true, "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash)
			}
			if err := pm.handleReceipt(peer, event); err != nil {
				debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_handle_failed", "protocol_ok", false, "store_ok", false, "err", err)
				return err
			}
		case gossipv1.EventTypeRelayIngest, gossipv1.EventTypeUntrustedRelay:
			count, sample, hash := gossipv1.ItemIDSample(event.ItemIDs, debugItemSampleLimit)
			if event.Type == gossipv1.EventTypeUntrustedRelay {
				debug.Log("in", gossipv1.FrameTypeRelayIngest, "relay_ingest_untrusted", "transport_ok", true, "protocol_ok", true, "store_ok", false, "item_count", count, "item_ids_sample", sample, "item_ids_hash", hash)
			} else {
				debug.Log("in", gossipv1.FrameTypeRelayIngest, "relay_ingest_received", "transport_ok", true, "protocol_ok", true, "store_ok", false, "item_count", count, "item_ids_sample", sample, "item_ids_hash", hash)
			}
		case gossipv1.EventTypeIgnored:
			debug.Log("in", event.FrameType, "frame_ignored", "transport_ok", true, "protocol_ok", true)
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
			debug.Log("local", event.FrameType, "observer_non_fatal", "protocol_ok", true, "err", err)
		}
	}

	return nil
}

func (pm *PeerManager) readLoop(peer *Peer) {
	debug := pm.peerDebugLogger(peer)
	debug.Log("local", "SESSION", "session_state", "state", "read_loop_started")

	defer func() {
		debug.Log("local", "SESSION", "session_state", "state", "read_loop_stopped")
		pm.removePeer(peer)
		_ = peer.Conn.Close()
	}()

	for {
		msgType, data, err := peer.Conn.ReadMessage()
		if err != nil {
			log.Printf("federation: read error from %s: %v", peer.URL, err)
			debug.Log("in", "CONNECTION", "recv_failed", "transport_ok", false, "protocol_ok", false, "close_reason", closeReasonFromError(err), "err", err)
			return
		}
		if msgType != websocket.BinaryMessage {
			log.Printf("federation: non-binary frame from %s type=%d", peer.URL, msgType)
			debug.Log("in", "NON_BINARY", "recv_rejected", "msg_type_raw", msgType, "bytes_in", len(data), "transport_ok", true, "protocol_ok", false)
			return
		}
		debug.Log("in", gossipv1.FrameTypeFromPrefixedBinaryFrame(data), "received", "bytes_in", len(data), "transport_ok", true)

		peer.adapterMu.Lock()
		adapter := peer.adapter
		peer.adapterMu.Unlock()
		if adapter == nil {
			log.Printf("federation: missing adapter for peer %s", peer.ID)
			debug.Log("local", "SESSION", "adapter_missing", "protocol_ok", false)
			return
		}

		events := adapter.PushInbound(data)
		if err := pm.processEvents(peer, adapter, events); err != nil {
			log.Printf("federation: fatal validation error peer=%s err=%v", peer.ID, err)
			debug.Log("in", adapter.LastFrameType(), "process_failed", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", err)
			return
		}
	}
}

func (pm *PeerManager) writeLoop(peer *Peer) {
	debug := pm.peerDebugLogger(peer)
	debug.Log("local", "SESSION", "session_state", "state", "write_loop_started")

	ticker := time.NewTicker(HeartbeatInterval)
	defer func() {
		ticker.Stop()
		debug.Log("local", "SESSION", "session_state", "state", "write_loop_stopped")
	}()

	for {
		select {
		case <-peer.Done:
			debug.Log("local", "SESSION", "session_state", "state", "peer_done_closed")
			return
		case <-ticker.C:
			if err := pm.writePeerMessage(peer, websocket.PingMessage, nil); err != nil {
				debug.Log("out", "PING", "heartbeat_failed", "transport_ok", false, "err", err)
				return
			}
			debug.Log("out", "PING", "heartbeat_sent", "bytes_out", 0, "transport_ok", true)
		}
	}
}

func (pm *PeerManager) addPeer(peer *Peer) {
	debug := pm.peerDebugLogger(peer)
	pm.peersMu.Lock()
	pm.peers[peer.ID] = peer
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	if peer.Metrics == nil {
		peer.Metrics = model.NewPeerMetrics(peer.ID)
	}
	pm.peerMetrics[peer.ID] = peer.Metrics
	pm.metricsMu.Unlock()

	localAddr, remoteAddr := peerSocketAddresses(peer)
	debug.Log("local", "CONNECTION", "session_state", "state", "peer_registered", "peer_id", peer.ID, "outbound", peer.Outbound, "local_addr", localAddr, "remote_addr", remoteAddr)
}

func (pm *PeerManager) removePeer(peer *Peer) {
	if peer == nil {
		return
	}
	debug := pm.peerDebugLogger(peer)

	peer.doneOnce.Do(func() { close(peer.Done) })

	pm.peersMu.Lock()
	delete(pm.peers, peer.ID)
	pm.peersMu.Unlock()

	pm.metricsMu.Lock()
	delete(pm.peerMetrics, peer.ID)
	pm.metricsMu.Unlock()
	debug.Log("local", "CONNECTION", "session_state", "state", "peer_removed", "peer_id", peer.ID)

	if peer.Outbound {
		debug.Log("local", "CONNECTION", "session_state", "state", "reconnect_scheduled", "url", peer.URL)
		select {
		case <-pm.ctxStop:
			debug.Log("local", "CONNECTION", "session_state", "state", "reconnect_skipped_stop")
		case pm.outbound <- peer.URL:
			debug.Log("local", "CONNECTION", "session_state", "state", "reconnect_enqueued")
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
		SessionID:   gossipv1.NewDebugSessionID("federation-in"),
		URL:         r.RemoteAddr,
		Conn:        conn,
		ConnectedAt: time.Now(),
		Health:      PeerHealth{LastSeen: time.Now()},
		Done:        make(chan struct{}),
		Metrics:     model.NewPeerMetrics(r.RemoteAddr),
	}
	debug := pm.peerDebugLogger(peer)
	localAddr, remoteAddr := peerSocketAddresses(peer)
	debug.Log("in", "CONNECTION", "session_state", "state", "accepted_upgrade", "http_remote_addr", r.RemoteAddr, "local_addr", localAddr, "remote_addr", remoteAddr)

	if err := pm.acceptInboundEncounter(peer); err != nil {
		log.Printf("federation: inbound encounter failed from %s: %v", r.RemoteAddr, err)
		debug.Log("local", "CONNECTION", "session_state", "state", "encounter_failed", "err", err)
		_ = conn.Close()
		return
	}

	debug.Log("local", "CONNECTION", "session_state", "state", "encounter_established")
	pm.inbound <- peer
}

func (pm *PeerManager) acceptInboundEncounter(peer *Peer) error {
	debug := pm.peerDebugLogger(peer)
	debug.Log("local", "SESSION", "session_state", "state", "inbound_encounter_start")

	localHello := gossipv1.BuildRelayHello(pm.relayID)
	adapter := gossipv1.NewSessionAdapter(localHello, true)

	if err := peer.Conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		debug.Log("local", "SESSION", "deadline_set_failed", "err", err)
		return err
	}
	msgType, data, err := peer.Conn.ReadMessage()
	if err != nil {
		debug.Log("in", gossipv1.FrameTypeHello, "recv_failed", "transport_ok", false, "protocol_ok", false, "close_reason", closeReasonFromError(err), "err", err)
		return err
	}
	if msgType != websocket.BinaryMessage {
		debug.Log("in", "NON_BINARY", "recv_rejected", "msg_type_raw", msgType, "bytes_in", len(data), "transport_ok", true, "protocol_ok", false)
		return errors.New("expected binary gossip frame")
	}
	debug.Log("in", gossipv1.FrameTypeFromPrefixedBinaryFrame(data), "received", "bytes_in", len(data), "transport_ok", true)

	events := adapter.PushInbound(data)
	if err := firstFatal(events); err != nil {
		debug.Log("in", adapter.LastFrameType(), "protocol_fatal", "transport_ok", true, "protocol_ok", false, "err", err)
		return err
	}

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		debug.Log("out", gossipv1.FrameTypeHello, "encode_failed", "transport_ok", false, "protocol_ok", false, "err", err)
		return err
	}
	if err := pm.writePeerMessage(peer, websocket.BinaryMessage, helloBytes); err != nil {
		debug.Log("out", gossipv1.FrameTypeHello, "send_failed", "bytes_out", len(helloBytes), "transport_ok", false, "protocol_ok", false, "err", err)
		return err
	}
	debug.Log("out", gossipv1.FrameTypeHello, "sent", "bytes_out", len(helloBytes), "transport_ok", true, "protocol_ok", true)
	peer.localHelloSent = true

	if err := pm.processEvents(peer, adapter, events); err != nil {
		debug.Log("in", adapter.LastFrameType(), "process_failed", "transport_ok", true, "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	if !adapter.IsHealthy() {
		debug.Log("local", "SESSION", "session_unhealthy", "state", "post_hello", "protocol_ok", false)
		return errors.New("gossip session unhealthy after inbound HELLO")
	}
	debug.Log("local", "SESSION", "session_state", "state", "healthy")

	peer.adapterMu.Lock()
	peer.adapter = adapter
	peer.adapterMu.Unlock()

	_ = peer.Conn.SetReadDeadline(time.Time{})
	go pm.readLoop(peer)
	go pm.writeLoop(peer)
	debug.Log("local", "SESSION", "session_state", "state", "loops_started")
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

func (pm *PeerManager) writePeerMessage(peer *Peer, messageType int, payload []byte) error {
	if peer == nil || peer.Conn == nil {
		return errors.New("federation: peer connection is not available")
	}

	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	return peer.Conn.WriteMessage(messageType, payload)
}

func (pm *PeerManager) sendEnvelope(peer *Peer, frameType string, payload any) error {
	debug := pm.peerDebugLogger(peer)

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

	metadata := make([]any, 0, 10)
	switch frameType {
	case gossipv1.FrameTypeSummary:
		preview := summaryPreviewFromPayload(payload)
		if len(preview) > 0 {
			count, sample, hash := gossipv1.ItemIDSample(preview, debugItemSampleLimit)
			metadata = append(metadata,
				"item_count", count,
				"preview_count", count,
				"item_ids_sample", sample,
				"item_ids_hash", hash,
			)
		}
	case gossipv1.FrameTypeRequest:
		if request, ok := payload.(gossipv1.RequestPayload); ok {
			count, sample, hash := gossipv1.ItemIDSample(request.Want, debugItemSampleLimit)
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
			count, sample, hash := gossipv1.ItemIDSample(ids, debugItemSampleLimit)
			metadata = append(metadata,
				"object_count", count,
				"accepted_item_ids_sample", sample,
				"accepted_item_ids_hash", hash,
			)
		}
	case gossipv1.FrameTypeReceipt:
		if receipt, ok := payload.(gossipv1.ReceiptPayload); ok {
			acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(receipt.Accepted, debugItemSampleLimit)
			metadata = append(metadata,
				"item_count", acceptedCount,
				"accepted_count", acceptedCount,
				"accepted_item_ids_sample", acceptedSample,
				"accepted_item_ids_hash", acceptedHash,
			)
		}
	}

	if err := pm.writePeerMessage(peer, websocket.BinaryMessage, prefixed); err != nil {
		logFields := make([]any, 0, len(metadata)+8)
		logFields = append(logFields, metadata...)
		logFields = append(logFields, "bytes_out", len(prefixed), "transport_ok", false, "protocol_ok", true, "store_ok", true, "err", err)
		debug.Log("out", frameType, "send_failed", logFields...)
		return err
	}

	logFields := make([]any, 0, len(metadata)+6)
	logFields = append(logFields, metadata...)
	logFields = append(logFields, "bytes_out", len(prefixed), "transport_ok", true, "protocol_ok", true, "store_ok", true)
	debug.Log("out", frameType, "sent", logFields...)
	return nil
}

func (pm *PeerManager) sendSummary(peer *Peer) error {
	debug := pm.peerDebugLogger(peer)

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

	eligibleIDs, err := pm.listSummaryEligibleIDs(context.Background())
	if err != nil {
		debug.Log("local", gossipv1.FrameTypeSummary, "summary_inventory_failed", "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	preview := append([]string(nil), eligibleIDs...)
	if uint64(len(preview)) > gossipv1.MaxSummaryPreviewItems {
		preview = preview[:gossipv1.MaxSummaryPreviewItems]
	}
	canonicalSummary := gossipv1.BuildSummaryPreviewPayload(eligibleIDs, preview, "")
	canonicalSummary.ItemCount = uint64(len(eligibleIDs))
	count, sample, hash := gossipv1.ItemIDSample(canonicalSummary.PreviewItemIDs, debugItemSampleLimit)
	debug.Log(
		"out",
		gossipv1.FrameTypeSummary,
		"summary_constructed",
		"summary_path", "federation.sendSummary:envelope_store->summary_preview",
		"source_inventory_count", len(canonicalSummary.PreviewItemIDs),
		"item_count", canonicalSummary.ItemCount,
		"bloom_bytes", len(canonicalSummary.BloomFilter),
		"item_ids_sample", sample,
		"item_ids_hash", hash,
		"preview_count", count,
	)
	if err := pm.sendEnvelope(peer, gossipv1.FrameTypeSummary, canonicalSummary); err != nil {
		return err
	}
	debug.Log("local", gossipv1.FrameTypeSummary, "summary_state", "state", "sent_once")
	peer.summarySent = true
	return nil
}

func (pm *PeerManager) handleSummary(peer *Peer, event gossipv1.Event) error {
	debug := pm.peerDebugLogger(peer)

	if event.Summary == nil {
		return errors.New("gossipv1: summary event missing payload")
	}
	if err := validateSummaryPreviewCursor(event.Summary.PreviewItemIDs, event.Summary.PreviewCursor); err != nil {
		return err
	}
	want, err := pm.computeMissingWant(context.Background(), event.Summary, nil, peer.RemoteMaxWant, debug)
	if err != nil {
		debug.Log("in", gossipv1.FrameTypeSummary, "summary_compute_want_failed", "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	wantCount, wantSample, wantHash := gossipv1.ItemIDSample(want, debugItemSampleLimit)
	debug.Log("local", gossipv1.FrameTypeSummary, "summary_computed_want", "requested_count", wantCount, "item_ids_sample", wantSample, "item_ids_hash", wantHash, "store_ok", true)
	if len(want) == 0 {
		debug.Log("local", gossipv1.FrameTypeSummary, "summary_no_request_needed", "requested_count", 0)
		return nil
	}
	return pm.sendEnvelope(peer, gossipv1.FrameTypeRequest, gossipv1.RequestPayload{Want: want})
}

func (pm *PeerManager) handleRequest(peer *Peer, event gossipv1.Event) error {
	debug := pm.peerDebugLogger(peer)

	if event.Request == nil {
		return errors.New("gossipv1: request event missing payload")
	}
	if len(event.Request.Want) == 0 {
		debug.Log("in", gossipv1.FrameTypeRequest, "request_empty", "requested_count", 0, "protocol_ok", true)
		return nil
	}

	objects, expectedReceiptIDs, err := pm.buildTransferObjects(context.Background(), event.Request.Want, debug)
	if err != nil {
		debug.Log("local", gossipv1.FrameTypeTransfer, "request_build_transfer_failed", "protocol_ok", false, "store_ok", false, "err", err)
		return err
	}
	transferCount, transferSample, transferHash := gossipv1.ItemIDSample(expectedReceiptIDs, debugItemSampleLimit)
	debug.Log("out", gossipv1.FrameTypeTransfer, "request_serviced", "object_count", len(objects), "expected_receipt_count", transferCount, "item_ids_sample", transferSample, "item_ids_hash", transferHash, "store_ok", true)

	peer.adapterMu.Lock()
	if peer.adapter != nil {
		peer.adapter.SetExpectedReceipt(expectedReceiptIDs)
	}
	peer.adapterMu.Unlock()

	return pm.sendEnvelope(peer, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: objects})
}

func (pm *PeerManager) handleTransfer(peer *Peer, event gossipv1.Event) error {
	debug := pm.peerDebugLogger(peer)
	traceCtx := gossipv1.WithDebugTrace(context.Background(), peer.SessionID, peerDebugIdentity(peer))

	if event.Transfer == nil {
		return errors.New("gossipv1: transfer event missing payload")
	}
	if pm.engine == nil {
		return errors.New("federation: relay forward engine is not configured")
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

		result, err := pm.engine.AcceptRelayForward(traceCtx, peer.RemoteNodeID, msg, relayForwardMaxPayloadBytes)
		if err != nil {
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", indexed.Object.ItemID, "reason", "ingest_failed", "detail", relayForwardErrorReason(err), "store_ok", false, "err", err)
			rejectedIDs = append(rejectedIDs, indexed.Object.ItemID)
			continue
		}

		switch result.Status {
		case storeforward.RelayForwardAccepted, storeforward.RelayForwardDuplicate, storeforward.RelayForwardSeenLoop:
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_accepted", "object_index", indexed.Index, "item_id", msg.ID, "store_ok", true)
			receipt.Accepted = append(receipt.Accepted, msg.ID)
		case storeforward.RelayForwardExpired:
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", msg.ID, "reason", "policy_reject", "detail", "object already expired", "store_ok", false)
			rejectedIDs = append(rejectedIDs, msg.ID)
		case storeforward.RelayForwardTooLarge:
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", msg.ID, "reason", "policy_reject", "detail", "payload exceeds relay limit", "store_ok", false)
			rejectedIDs = append(rejectedIDs, msg.ID)
		case storeforward.RelayForwardInvalid:
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", msg.ID, "reason", "policy_reject", "detail", "invalid transfer object", "store_ok", false)
			rejectedIDs = append(rejectedIDs, msg.ID)
		default:
			debug.Log("in", gossipv1.FrameTypeTransfer, "transfer_object_rejected", "object_index", indexed.Index, "item_id", msg.ID, "reason", "ingest_failed", "detail", "relay forward rejected", "store_ok", false)
			rejectedIDs = append(rejectedIDs, msg.ID)
		}
	}

	if len(receipt.Accepted) > 1 {
		gossipv1.SortDigestHexIDs(receipt.Accepted)
	}
	acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(receipt.Accepted, debugItemSampleLimit)
	rejectedCount, rejectedSample, rejectedHash := gossipv1.ItemIDSample(rejectedIDs, debugItemSampleLimit)
	debug.Log("out", gossipv1.FrameTypeReceipt, "receipt_constructed", "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash, "rejected_count", rejectedCount, "rejected_item_ids_sample", rejectedSample, "rejected_item_ids_hash", rejectedHash)

	return pm.sendEnvelope(peer, gossipv1.FrameTypeReceipt, receipt)
}

func (pm *PeerManager) handleReceipt(peer *Peer, event gossipv1.Event) error {
	debug := pm.peerDebugLogger(peer)

	if event.Receipt == nil {
		return errors.New("gossipv1: receipt event missing payload")
	}
	acceptedCount, acceptedSample, acceptedHash := gossipv1.ItemIDSample(event.Receipt.Accepted, debugItemSampleLimit)
	debug.Log("in", gossipv1.FrameTypeReceipt, "receipt_processed", "accepted_count", acceptedCount, "accepted_item_ids_sample", acceptedSample, "accepted_item_ids_hash", acceptedHash, "store_ok", true)
	return nil
}

func (pm *PeerManager) listSummaryEligibleIDs(ctx context.Context) ([]string, error) {
	if pm.envelopeStore == nil {
		return nil, errors.New("federation: envelope store is required for summary preview inventory")
	}

	return pm.envelopeStore.GetAllEnvelopeIDs(ctx)
}

func (pm *PeerManager) computeMissingWant(ctx context.Context, summary *gossipv1.SummaryPayload, candidateIDs []string, peerMaxWant uint64, debugLoggers ...gossipv1.DebugSessionLogger) ([]string, error) {
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

		known, knownErr := pm.hasObjectID(ctx, id)
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

		known, knownErr := pm.hasObjectID(ctx, id)
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

func (pm *PeerManager) hasObjectID(ctx context.Context, id string) (bool, error) {
	if pm.envelopeStore != nil {
		envelope, err := pm.envelopeStore.GetEnvelopeByID(ctx, id)
		if err != nil {
			if !isNotFoundError(err) {
				return false, err
			}
		} else if envelope != nil {
			return true, nil
		}
	}
	if pm.store == nil {
		return false, nil
	}

	msg, err := pm.store.GetMessageByID(ctx, id)
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return msg != nil, nil
}

func validateSummaryPreviewCursor(previewIDs []string, previewCursor string) error {
	if len(previewIDs) == 0 {
		if previewCursor != "" {
			return errors.New("gossipv1: summary preview_cursor must be absent when preview_item_ids is empty")
		}
		return nil
	}

	if previewCursor == "" {
		return errors.New("gossipv1: summary preview_cursor is required when preview_item_ids are present")
	}
	if previewCursor != previewIDs[len(previewIDs)-1] {
		return errors.New("gossipv1: summary preview_cursor must equal last preview_item_ids element")
	}
	return nil
}

func (pm *PeerManager) buildTransferObjects(ctx context.Context, want []string, debug gossipv1.DebugSessionLogger) ([]gossipv1.TransferObject, []string, error) {
	if pm.store == nil {
		return []gossipv1.TransferObject{}, []string{}, nil
	}
	objects := make([]gossipv1.TransferObject, 0, len(want))
	ids := make([]string, 0, len(want))
	now := time.Now().UTC()

	for _, id := range want {
		if uint64(len(objects)) >= gossipv1.MaxTransferItems {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, id, "request_service_decision", "decision", "skipped", "reason", "max_transfer_limit_reached", "store_ok", true)
			break
		}

		object, found, err := pm.loadTransferObjectByItemID(ctx, id)
		if err != nil {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, id, "request_service_decision", "decision", "skipped", "reason", "lookup_failed", "store_ok", false, "err", err)
			return nil, nil, err
		}
		if !found {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, id, "request_service_decision", "decision", "missing", "reason", "nil_message", "store_ok", true)
			continue
		}
		if object.ExpiryUnixMS <= uint64(now.UnixMilli()) {
			debug.LogItem("out", gossipv1.FrameTypeTransfer, id, "request_service_decision", "decision", "skipped", "reason", "expired", "store_ok", true)
			continue
		}

		objects = append(objects, object)
		ids = append(ids, object.ItemID)
		debug.LogItem("out", gossipv1.FrameTypeTransfer, object.ItemID, "request_service_decision", "decision", "sent", "reason", "found", "store_ok", true)
	}

	return objects, ids, nil
}

func transferObjectToMessage(object gossipv1.TransferObject) (*model.Message, error) {
	if object.ItemID == "" {
		return nil, errors.New("item_id is required")
	}
	if !gossipv1.IsDigestHexID(object.ItemID) {
		return nil, errors.New("item_id must be 64 lowercase hex chars")
	}
	if object.HopCount > 65535 {
		return nil, errors.New("hop_count must be <= 65535")
	}
	envelope, err := gossipv1.DecodeItemEnvelopeB64(object.EnvelopeB64)
	if err != nil {
		return nil, fmt.Errorf("decode envelope_b64: %w", err)
	}
	if envelope.To == "" {
		return nil, errors.New("envelope destination is required")
	}
	if envelope.From == "" {
		return nil, errors.New("envelope manifest/source is required")
	}
	if _, err := model.DecodePayloadB64(envelope.PayloadB64); err != nil {
		return nil, errors.New("invalid payload_b64")
	}
	if envelope.ExpiresAt <= envelope.CreatedAt {
		if envelope.ExpiresAt > 0 {
			return nil, errors.New("expires_at must be greater than created_at")
		}
	}
	if envelope.ExpiresAt > 0 && object.ExpiryUnixMS != uint64(envelope.ExpiresAt)*1000 {
		return nil, errors.New("expiry_unix_ms must equal envelope expiry")
	}
	if object.ItemID != gossipv1.ComputeTransferObjectItemID(object) {
		return nil, errors.New("item_id must equal sha256(envelope bytes)")
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
			return nil, errors.New("object already expired")
		}
		createdAt = expiresAt.Add(-time.Second)
	}

	return &model.Message{
		ID:        object.ItemID,
		From:      envelope.From,
		To:        envelope.To,
		Payload:   envelope.PayloadB64,
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

func canonicalTransferObjectFromMessage(msg *model.Message) (gossipv1.TransferObject, bool) {
	if msg == nil {
		return gossipv1.TransferObject{}, false
	}
	if !gossipv1.IsDigestHexID(msg.ID) {
		return gossipv1.TransferObject{}, false
	}

	createdAt := msg.CreatedAt.UTC().Unix()
	expiresAt := msg.ExpiresAt.UTC().Unix()
	if envelopeB64, err := gossipv1.EncodeTransferEnvelopeB64(msg.To, msg.From, msg.Payload); err == nil {
		candidate := gossipv1.TransferObject{
			ItemID:       msg.ID,
			EnvelopeB64:  envelopeB64,
			ExpiryUnixMS: uint64(msg.ExpiresAt.UTC().UnixMilli()),
			HopCount:     0,
			Envelope: gossipv1.ItemEnvelope{
				From:       msg.From,
				To:         msg.To,
				PayloadB64: msg.Payload,
				CreatedAt:  createdAt,
				ExpiresAt:  expiresAt,
			},
		}
		if gossipv1.ComputeTransferObjectItemID(candidate) == msg.ID {
			return candidate, true
		}
	}

	if msg.ID != gossipv1.ComputeItemID(msg.From, msg.To, msg.Payload, createdAt, expiresAt) {
		return gossipv1.TransferObject{}, false
	}
	envelopeB64, err := gossipv1.EncodeItemEnvelopeB64(msg.From, msg.To, msg.Payload, createdAt, expiresAt)
	if err != nil {
		return gossipv1.TransferObject{}, false
	}

	return gossipv1.TransferObject{
		ItemID:       msg.ID,
		EnvelopeB64:  envelopeB64,
		ExpiryUnixMS: uint64(msg.ExpiresAt.UTC().UnixMilli()),
		HopCount:     0,
		Envelope: gossipv1.ItemEnvelope{
			From:       msg.From,
			To:         msg.To,
			PayloadB64: msg.Payload,
			CreatedAt:  createdAt,
			ExpiresAt:  expiresAt,
		},
	}, true
}

func (pm *PeerManager) loadTransferObjectByItemID(ctx context.Context, itemID string) (gossipv1.TransferObject, bool, error) {
	if !gossipv1.IsDigestHexID(itemID) {
		return gossipv1.TransferObject{}, false, nil
	}

	msg, err := pm.store.GetMessageByID(ctx, itemID)
	if err != nil {
		if isNotFoundError(err) {
			return gossipv1.TransferObject{}, false, nil
		}
		return gossipv1.TransferObject{}, false, err
	}
	if msg == nil {
		return gossipv1.TransferObject{}, false, nil
	}

	object, ok := canonicalTransferObjectFromMessage(msg)
	if !ok {
		return gossipv1.TransferObject{}, false, nil
	}

	return object, true, nil
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
		if !gossipv1.IsDigestHexID(id) {
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
	have, _ = gossipv1.NormalizeDigestHexIDs(have)
	if uint64(len(have)) > gossipv1.MaxSummaryPreviewItems {
		have = have[:gossipv1.MaxSummaryPreviewItems]
	}

	canonicalSummary := gossipv1.BuildSummaryPreviewPayload(have, have, "")
	haveCount, haveSample, haveHash := gossipv1.ItemIDSample(canonicalSummary.PreviewItemIDs, debugItemSampleLimit)
	gossipv1.NewDebugSessionLogger("federation-summary-forward", originNodeID).Log(
		"out",
		gossipv1.FrameTypeSummary,
		"summary_constructed",
		"summary_path", "federation.forwardSummaryToPeers:ids->summary_preview",
		"source_inventory_count", len(canonicalSummary.PreviewItemIDs),
		"item_count", canonicalSummary.ItemCount,
		"bloom_bytes", len(canonicalSummary.BloomFilter),
		"item_ids_sample", haveSample,
		"item_ids_hash", haveHash,
		"preview_count", haveCount,
	)

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
		debug := pm.peerDebugLogger(peer)
		if err := pm.sendEnvelope(peer, gossipv1.FrameTypeSummary, canonicalSummary); err != nil {
			log.Printf("federation: failed forwarding summary to peer=%s err=%v", peer.ID, err)
			debug.Log("out", gossipv1.FrameTypeSummary, "summary_forward_failed", "transport_ok", false, "protocol_ok", true, "store_ok", true, "err", err)
			continue
		}
		debug.Log("out", gossipv1.FrameTypeSummary, "summary_forwarded", "transport_ok", true, "protocol_ok", true, "store_ok", true, "origin_node_id", originNodeID)
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
