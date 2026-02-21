package federation

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	// ProtocolVersion is the federation protocol version.
	ProtocolVersion = "1.0"

	// HeartbeatInterval is how often to send ping to peers.
	HeartbeatInterval = 30 * time.Second

	// InventoryGossipInterval is how often to gossip inventory.
	InventoryGossipInterval = 15 * time.Second

	// ReconnectBaseDelay is the base delay for exponential backoff.
	ReconnectBaseDelay = 1 * time.Second

	// ReconnectMaxDelay is the maximum delay for exponential backoff.
	ReconnectMaxDelay = 60 * time.Second

	// MaxForwardedPayloadSize is the maximum allowed byte length of a forwarded
	// message's base64-encoded payload.
	MaxForwardedPayloadSize = 64 * 1024
)

// Peer represents a connected relay peer.
type Peer struct {
	ID            string
	URL           string
	Outbound      bool // true if we dialed this peer (enables auto-reconnect)
	Conn          *websocket.Conn
	Send          chan []byte
	LastInventory time.Time
	ConnectedAt   time.Time
	Health        PeerHealth
	healthMu      sync.Mutex
	Done          chan struct{}
	doneOnce      sync.Once

	// Peer-specific metrics for scoring
	Metrics *model.PeerMetrics
	// TAR batcher for traffic analysis resistance
	Batcher *PeerBatcher
}

// PeerHealth tracks peer health metrics with failure tracking.
type PeerHealth struct {
	Uptime            time.Duration
	LastSeen          time.Time
	LastForwardAt     time.Time
	MessagesForwarded int
	MessagesReceived  int
	FailureCount      int
	BackoffUntil      time.Time
	IsHealthy         bool
}

// RecordFailure records a failure for exponential backoff.
func (h *PeerHealth) RecordFailure() {
	h.FailureCount++
	// Exponential backoff: 2^failure_count seconds, max 5 minutes
	backoff := time.Duration(min(h.FailureCount*h.FailureCount, 300)) * time.Second
	h.BackoffUntil = time.Now().Add(backoff)
	h.IsHealthy = false
}

// RecordSuccess records a successful operation.
func (h *PeerHealth) RecordSuccess() {
	h.FailureCount = 0
	h.BackoffUntil = time.Time{}
	h.IsHealthy = true
}

// IsBackingOff returns true if the peer is currently backing off from failures.
func (h *PeerHealth) IsBackingOff() bool {
	return time.Now().Before(h.BackoffUntil)
}

// PeerManager manages connections to relay peers.
type PeerManager struct {
	relayID    string
	peers      map[string]*Peer
	peersMu    sync.RWMutex
	store      store.Store
	clients    *model.ClientRegistry
	maxTTL     time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	inbound    chan *Peer
	outbound   chan string
	disconnect chan *Peer

	// TAR configuration
	tarConfig *TARConfig
	// Forwarding strategy configuration
	forwardingConfig *ForwardingConfig
	// Forwarding strategy
	forwardingStrategy *ForwardingStrategy
	// Peer metrics (for scoring)
	peerMetrics map[string]*model.PeerMetrics
	metricsMu   sync.RWMutex
}

// NewPeerManager creates a new peer manager.
func NewPeerManager(relayID string, store store.Store, clients *model.ClientRegistry, maxTTL time.Duration) *PeerManager {
	return NewPeerManagerWithConfig(relayID, store, clients, maxTTL, DefaultTARConfig(), DefaultForwardingConfig())
}

// NewPeerManagerWithConfig creates a new peer manager with explicit TAR and forwarding config.
func NewPeerManagerWithConfig(relayID string, store store.Store, clients *model.ClientRegistry, maxTTL time.Duration, tarConfig *TARConfig, forwardingConfig *ForwardingConfig) *PeerManager {
	ctx, cancel := context.WithCancel(context.Background())

	// Validate configs
	tarConfig.Validate()
	forwardingConfig.Validate()

	return &PeerManager{
		relayID:            relayID,
		peers:              make(map[string]*Peer),
		store:              store,
		clients:            clients,
		maxTTL:             maxTTL,
		ctx:                ctx,
		cancel:             cancel,
		inbound:            make(chan *Peer, 100),
		outbound:           make(chan string, 100),
		disconnect:         make(chan *Peer, 100),
		tarConfig:          tarConfig,
		forwardingConfig:   forwardingConfig,
		forwardingStrategy: NewForwardingStrategy(forwardingConfig),
		peerMetrics:        make(map[string]*model.PeerMetrics),
	}
}

// AddPeerURL adds a peer URL to connect to.
func (pm *PeerManager) AddPeerURL(url string) {
	pm.outbound <- url
}

// Run starts the peer manager.
func (pm *PeerManager) Run() {
	// Start gossip ticker
	go pm.gossipLoop()

	// Start peer handler loop
	for {
		select {
		case <-pm.ctx.Done():
			pm.cleanup()
			return
		case url := <-pm.outbound:
			go pm.dialPeer(url)
		case peer := <-pm.inbound:
			pm.addPeer(peer)
		case peer := <-pm.disconnect:
			pm.removePeer(peer)
		}
	}
}

// cleanup closes all peer connections.
func (pm *PeerManager) cleanup() {
	pm.peersMu.Lock()
	defer pm.peersMu.Unlock()
	for _, peer := range pm.peers {
		peer.doneOnce.Do(func() { close(peer.Done) })
		peer.Conn.Close()
	}
}

// dialPeer initiates an outbound connection to a peer.
func (pm *PeerManager) dialPeer(url string) {
	// Use exponential backoff for reconnection
	delay := ReconnectBaseDelay
	maxRetries := 10

	for i := 0; i < maxRetries; i++ {
		select {
		case <-pm.ctx.Done():
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

		peerID := uuid.New().String()
		peer := &Peer{
			ID:          peerID,
			URL:         url,
			Outbound:    true,
			Conn:        conn,
			Send:        make(chan []byte, 256),
			ConnectedAt: time.Now(),
			Health:      PeerHealth{LastSeen: time.Now()},
			Done:        make(chan struct{}),
		}

		// Send hello
		hello := model.RelayHelloFrame{
			Type:    model.FrameTypeRelayHello,
			RelayID: pm.relayID,
			Version: ProtocolVersion,
		}
		if err := conn.WriteJSON(hello); err != nil {
			log.Printf("federation: hello failed %s: %v", url, err)
			conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		// Wait for hello_ok
		var helloResp model.RelayHelloFrame
		if err := conn.ReadJSON(&helloResp); err != nil {
			log.Printf("federation: hello response failed %s: %v", url, err)
			conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		if helloResp.Type != model.FrameTypeRelayOK {
			log.Printf("federation: unexpected handshake response type %q from %s, expected %q", helloResp.Type, url, model.FrameTypeRelayOK)
			conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		// Use the peer's relay_id as its canonical identifier if provided.
		if helloResp.RelayID != "" {
			peer.ID = helloResp.RelayID
		}

		log.Printf("federation: connected to peer %s (relay_id: %s)", url, peer.ID)

		// Start peer loops
		go pm.readLoop(peer)
		go pm.writeLoop(peer)

		// Register peer
		pm.inbound <- peer
		return
	}

	log.Printf("federation: max retries exceeded for %s", url)

	// After max retries, don't give up permanently. Instead, periodically
	// re-enqueue this URL for another connection attempt while the
	// PeerManager context is still active.
	go func(url string, initialDelay time.Duration) {
		// Use a conservative retry interval based on the last backoff delay,
		// but ensure it is at least the base delay to avoid tight loops.
		retryInterval := initialDelay
		if retryInterval < ReconnectBaseDelay {
			retryInterval = ReconnectBaseDelay
		}

		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()

		for {
			select {
			case <-pm.ctx.Done():
				return
			case <-ticker.C:
				select {
				case <-pm.ctx.Done():
					return
				case pm.outbound <- url:
					// URL re-enqueued for another dialPeer attempt.
				}
			}
		}
	}(url, delay)
}

// readLoop reads messages from a peer.
func (pm *PeerManager) readLoop(peer *Peer) {
	defer func() {
		pm.disconnect <- peer
		peer.Conn.Close()
	}()

	for {
		var rawMsg json.RawMessage
		err := peer.Conn.ReadJSON(&rawMsg)
		if err != nil {
			log.Printf("federation: read error from %s: %v", peer.URL, err)
			return
		}

		// Parse frame type
		var frameType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawMsg, &frameType); err != nil {
			continue
		}

		peer.healthMu.Lock()
		peer.Health.LastSeen = time.Now()
		peer.healthMu.Unlock()

		switch frameType.Type {
		case model.FrameTypeRelayHello:
			var frame model.RelayHelloFrame
			if err := json.Unmarshal(rawMsg, &frame); err != nil {
				continue
			}
			pm.handleRelayHello(peer, &frame)
		case model.FrameTypeRelayInventory:
			var frame model.RelayInventoryFrame
			if err := json.Unmarshal(rawMsg, &frame); err != nil {
				continue
			}
			pm.handleRelayInventory(peer, &frame)
		case model.FrameTypeRelayRequest:
			var frame model.RelayRequestFrame
			if err := json.Unmarshal(rawMsg, &frame); err != nil {
				continue
			}
			pm.handleRelayRequest(peer, &frame)
		case model.FrameTypeRelayForward:
			var frame model.RelayForwardFrame
			if err := json.Unmarshal(rawMsg, &frame); err != nil {
				continue
			}
			pm.handleRelayForward(peer, &frame)
		case model.FrameTypeRelayAck:
			var frame model.RelayAckFrame
			if err := json.Unmarshal(rawMsg, &frame); err != nil {
				continue
			}
			pm.handleRelayAck(peer, &frame)
		case model.FrameTypeRelayCover:
			// Cover frames are relay-to-relay only, do not process or forward
			// Just update last seen
		}
	}
}

// writeLoop writes messages to a peer.
func (pm *PeerManager) writeLoop(peer *Peer) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer func() {
		ticker.Stop()
		peer.Conn.Close()
	}()

	for {
		select {
		case <-peer.Done:
			return
		case <-ticker.C:
			peer.Conn.WriteMessage(websocket.PingMessage, nil)
		case msg, ok := <-peer.Send:
			if !ok {
				peer.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := peer.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
}

// addPeer adds a peer to the manager.
func (pm *PeerManager) addPeer(peer *Peer) {
	pm.peersMu.Lock()
	defer pm.peersMu.Unlock()
	pm.peers[peer.ID] = peer

	// Initialize peer metrics for scoring
	pm.metricsMu.Lock()
	peer.Metrics = model.NewPeerMetrics(peer.ID)
	pm.peerMetrics[peer.ID] = peer.Metrics
	pm.metricsMu.Unlock()

	// Initialize TAR batcher
	peer.Batcher = NewPeerBatcher(peer.ID, 256, pm.tarConfig)
	peer.Batcher.Start(func(frame []byte) {
		select {
		case peer.Send <- frame:
		default:
			metrics.IncrementDropped()
		}
	})

	log.Printf("federation: peer added %s (total: %d)", peer.ID, len(pm.peers))
	metrics.SetPeersConnected(len(pm.peers))
}

// removePeer removes a peer from the manager.
func (pm *PeerManager) removePeer(peer *Peer) {
	pm.peersMu.Lock()
	delete(pm.peers, peer.ID)

	// Update metrics
	pm.metricsMu.Lock()
	if peer.Metrics != nil {
		peer.Metrics.RecordDisconnect()
	}
	pm.metricsMu.Unlock()

	// Stop batcher
	if peer.Batcher != nil {
		peer.Batcher.Stop()
	}

	log.Printf("federation: peer removed %s (total: %d)", peer.ID, len(pm.peers))
	pm.peersMu.Unlock()

	metrics.SetPeersConnected(len(pm.peers))

	// Remove peer metrics
	pm.metricsMu.Lock()
	delete(pm.peerMetrics, peer.ID)
	pm.metricsMu.Unlock()

	// Re-enqueue outbound peers for reconnection while the manager is active.
	if peer.Outbound {
		select {
		case <-pm.ctx.Done():
		case pm.outbound <- peer.URL:
		}
	}
}

// handleRelayHello handles incoming relay hello.
func (pm *PeerManager) handleRelayHello(peer *Peer, frame *model.RelayHelloFrame) {
	// Send hello_ok
	resp := map[string]string{
		"type":     model.FrameTypeRelayOK,
		"relay_id": pm.relayID,
	}
	if err := peer.Conn.WriteJSON(resp); err != nil {
		log.Printf("federation: failed to send relay_ok to peer %s: %v", peer.ID, err)
		_ = peer.Conn.Close()
	}
}

// handleRelayInventory handles inventory gossip from peers.
func (pm *PeerManager) handleRelayInventory(peer *Peer, frame *model.RelayInventoryFrame) {
	recipientID := frame.RecipientID
	messageIDs := frame.MessageIDs

	if recipientID == "" || len(messageIDs) == 0 {
		return
	}

	peer.healthMu.Lock()
	peer.Health.LastSeen = time.Now()
	peer.healthMu.Unlock()

	// Get local message IDs for this recipient
	localIDs, err := pm.getLocalMessageIDs(recipientID)
	if err != nil {
		log.Printf("federation: get local ids failed: %v", err)
		return
	}

	// Compute diff: what peer has that we don't
	missingFromLocal := diff(messageIDs, localIDs)

	if len(missingFromLocal) > 0 {
		// Request missing messages from peer
		request := model.RelayRequestFrame{
			Type:       model.FrameTypeRelayRequest,
			MessageIDs: missingFromLocal,
		}
		data, err := json.Marshal(request)
		if err != nil {
			log.Printf("federation: failed to marshal relay request: %v", err)
			return
		}
		select {
		case peer.Send <- data:
		default:
			log.Printf("federation: peer send channel full")
		}
	}
}

// handleRelayRequest handles requests for full messages.
func (pm *PeerManager) handleRelayRequest(peer *Peer, frame *model.RelayRequestFrame) {
	messageIDs := frame.MessageIDs
	if len(messageIDs) == 0 {
		return
	}

	ctx := context.Background()
	for _, msgID := range messageIDs {
		msg, err := pm.store.GetMessageByID(ctx, msgID)
		if err != nil {
			continue
		}

		forward := model.RelayForwardFrame{
			Type:    model.FrameTypeRelayForward,
			Message: msg,
		}
		data, err := json.Marshal(forward)
		if err != nil {
			log.Printf("federation: failed to marshal relay forward for %s: %v", msgID, err)
			continue
		}
		select {
		case peer.Send <- data:
			peer.healthMu.Lock()
			peer.Health.MessagesForwarded++
			peer.healthMu.Unlock()
		default:
			log.Printf("federation: peer send channel full")
		}
	}
}

// handleRelayForward handles incoming forwarded messages.
func (pm *PeerManager) handleRelayForward(peer *Peer, frame *model.RelayForwardFrame) {
	msg := frame.Message
	if msg == nil || msg.ID == "" || msg.From == "" || msg.To == "" || msg.Payload == "" {
		return
	}
	if msg.CreatedAt.IsZero() || msg.ExpiresAt.IsZero() {
		return
	}
	// Reject oversized payloads.
	if len(msg.Payload) > MaxForwardedPayloadSize {
		log.Printf("federation: forwarded message %s payload too large (%d bytes), ignoring", msg.ID, len(msg.Payload))
		return
	}

	peer.healthMu.Lock()
	peer.Health.MessagesReceived++
	peer.healthMu.Unlock()

	// Dedupe: check if message already exists
	ctx := context.Background()
	if existing, err := pm.store.GetMessageByID(ctx, msg.ID); err == nil && existing != nil {
		log.Printf("federation: duplicate message %s, ignoring", msg.ID)
		return
	}

	// Verify TTL not expired
	if time.Now().After(msg.ExpiresAt) {
		log.Printf("federation: message %s already expired, ignoring", msg.ID)
		return
	}

	// Persist the message
	if err := pm.store.PersistMessage(ctx, msg); err != nil {
		log.Printf("federation: persist forwarded message failed: %v", err)
		return
	}

	log.Printf("federation: persisted forwarded message %s for %s", msg.ID, msg.To)

	// Deliver if recipient is online
	if pm.clients.IsOnline(msg.To) {
		pm.deliverMessage(msg)
	}
	// Do not re-gossip forwarded messages to avoid message loops.
}

// handleRelayAck handles relay acknowledgment frames.
// Updates peer metrics based on ack status.
func (pm *PeerManager) handleRelayAck(peer *Peer, frame *model.RelayAckFrame) {
	if peer.Metrics == nil {
		return
	}

	// Calculate latency based on forward time (would need envelope tracking in real impl)
	// For now, use a default good latency
	latencyMs := 100.0

	switch frame.Status {
	case "accepted":
		peer.Metrics.RecordAck(latencyMs)
		metrics.IncrementPeerAcks(peer.ID)
	case "duplicate":
		// Duplicate is not a failure, just log
		log.Printf("federation: duplicate ack from peer %s for envelope %s", peer.ID, frame.EnvelopeID)
	case "expired":
		// Expired is a soft failure
		peer.Metrics.RecordTimeout()
		metrics.IncrementPeerTimeouts(peer.ID)
	default:
		log.Printf("federation: unknown ack status from peer %s: %s", peer.ID, frame.Status)
	}

	// Update metrics
	metrics.SetPeerScore(peer.ID, peer.Metrics.Score)
}

// deliverMessage delivers a message to local recipients.
func (pm *PeerManager) deliverMessage(msg *model.Message) {
	recipients := pm.clients.GetClients(msg.To)
	for _, r := range recipients {
		data, _ := json.Marshal(model.WSFrame{
			Type:       model.FrameTypeMessage,
			MsgID:      msg.ID,
			From:       msg.From,
			PayloadB64: msg.Payload,
			At:         msg.CreatedAt.Unix(),
		})
		select {
		case r.Send <- data:
			pm.store.MarkDelivered(context.Background(), msg.ID, r.WayfarerID)
		default:
		}
	}
}

// gossipLoop periodically gossips inventory to peers.
func (pm *PeerManager) gossipLoop() {
	ticker := time.NewTicker(InventoryGossipInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.gossipInventory()
		}
	}
}

// gossipInventory sends inventory to all peers.
func (pm *PeerManager) gossipInventory() {
	pm.peersMu.RLock()
	peers := make([]*Peer, 0, len(pm.peers))
	for _, p := range pm.peers {
		peers = append(peers, p)
	}
	pm.peersMu.RUnlock()

	if len(peers) == 0 {
		return
	}

	// Get all recipients we have messages for
	recipients := pm.getRecipientsWithMessages()
	if len(recipients) == 0 {
		return
	}

	for _, peer := range peers {
		for _, recipientID := range recipients {
			messageIDs, err := pm.getLocalMessageIDs(recipientID)
			if err != nil || len(messageIDs) == 0 {
				continue
			}

			inv := model.RelayInventoryFrame{
				Type:        model.FrameTypeRelayInventory,
				RecipientID: recipientID,
				MessageIDs:  messageIDs,
			}
			data, err := json.Marshal(inv)
			if err != nil {
				log.Printf("federation: failed to marshal inventory for gossip: %v", err)
				continue
			}
			select {
			case peer.Send <- data:
			default:
			}
		}
		peer.LastInventory = time.Now()
	}
}

// gossipMessage announces a new message to peers.
func (pm *PeerManager) gossipMessage(msg *model.Message) {
	pm.peersMu.RLock()
	peers := make([]*Peer, 0, len(pm.peers))
	for _, p := range pm.peers {
		peers = append(peers, p)
	}
	pm.peersMu.RUnlock()

	if len(peers) == 0 {
		return
	}

	inv := model.RelayInventoryFrame{
		Type:        model.FrameTypeRelayInventory,
		RecipientID: msg.To,
		MessageIDs:  []string{msg.ID},
	}
	data, err := json.Marshal(inv)
	if err != nil {
		log.Printf("federation: failed to marshal inventory for gossip: %v", err)
		return
	}

	for _, peer := range peers {
		select {
		case peer.Send <- data:
		default:
		}
	}
}

// getLocalMessageIDs gets all message IDs for a recipient.
func (pm *PeerManager) getLocalMessageIDs(recipientID string) ([]string, error) {
	return pm.store.GetAllQueuedMessageIDs(context.Background(), recipientID)
}

// getRecipientsWithMessages gets all recipients with queued messages.
func (pm *PeerManager) getRecipientsWithMessages() []string {
	recipients, err := pm.store.GetAllRecipientIDs(context.Background())
	if err != nil {
		log.Printf("failed to get recipients with queued messages: %v", err)
		return nil
	}
	return recipients
}

// GetPeers returns a copy of current peers.
func (pm *PeerManager) GetPeers() map[string]PeerHealth {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()

	result := make(map[string]PeerHealth)
	for id, peer := range pm.peers {
		peer.healthMu.Lock()
		peer.Health.Uptime = time.Since(peer.ConnectedAt)
		health := peer.Health
		peer.healthMu.Unlock()
		result[id] = health
	}
	return result
}

// Stop stops the peer manager.
func (pm *PeerManager) Stop() {
	pm.cancel()
}

// federationUpgrader is used to upgrade inbound federation WebSocket connections.
var federationUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// HandleInboundPeer handles an inbound peer connection (from HTTP upgrade).
func (pm *PeerManager) HandleInboundPeer(w http.ResponseWriter, r *http.Request) {
	conn, err := federationUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("federation: upgrade failed: %v", err)
		return
	}

	peerID := uuid.New().String()
	peer := &Peer{
		ID:          peerID,
		URL:         r.RemoteAddr,
		Conn:        conn,
		Send:        make(chan []byte, 256),
		ConnectedAt: time.Now(),
		Health:      PeerHealth{LastSeen: time.Now()},
		Done:        make(chan struct{}),
	}

	// Read hello
	var hello model.RelayHelloFrame
	if err := conn.ReadJSON(&hello); err != nil {
		log.Printf("federation: inbound hello failed: %v", err)
		conn.Close()
		return
	}

	// Validate hello frame - reject connections that don't identify as a relay peer.
	if hello.Type != model.FrameTypeRelayHello || hello.RelayID == "" {
		log.Printf("federation: invalid hello from %s: type=%q relay_id=%q", r.RemoteAddr, hello.Type, hello.RelayID)
		conn.Close()
		return
	}

	// Use the peer's relay_id as its canonical identifier.
	peer.ID = hello.RelayID

	// Send hello_ok
	resp := map[string]string{
		"type":     model.FrameTypeRelayOK,
		"relay_id": pm.relayID,
	}
	if err := conn.WriteJSON(resp); err != nil {
		conn.Close()
		return
	}

	log.Printf("federation: inbound peer connected from %s (relay_id: %s)", r.RemoteAddr, peer.ID)

	// Start loops
	go pm.readLoop(peer)
	go pm.writeLoop(peer)

	pm.inbound <- peer
}

// AnnounceMessage announces a new message to federation.
func (pm *PeerManager) AnnounceMessage(msg *model.Message) {
	go pm.gossipMessage(msg)
}

// ForwardToPeers forwards a message to selected peers based on score.
// This implements score-based routing with topK selection and exploration.
func (pm *PeerManager) ForwardToPeers(msg *model.Message, originRelayID string) {
	// Get peer metrics
	pm.metricsMu.RLock()
	metricsCopy := make(map[string]*model.PeerMetrics, len(pm.peerMetrics))
	for id, m := range pm.peerMetrics {
		metricsCopy[id] = m
	}
	pm.metricsMu.RUnlock()

	// Select peers based on scoring
	selectedIDs := pm.forwardingStrategy.SelectPeers(metricsCopy, originRelayID)

	if len(selectedIDs) == 0 {
		log.Printf("federation: no selected peers to forward message %s", msg.ID)
		return
	}

	// Forward to selected peers using batcher
	for _, peerID := range selectedIDs {
		pm.peersMu.RLock()
		peer, ok := pm.peers[peerID]
		pm.peersMu.RUnlock()

		if !ok {
			continue
		}

		// Record forward in metrics
		if peer.Metrics != nil {
			peer.Metrics.RecordForward()
			metrics.SetPeerScore(peerID, peer.Metrics.Score)
		}

		// Create forward frame
		forward := model.RelayForwardFrame{
			Type:    model.FrameTypeRelayForward,
			Message: msg,
		}

		// Apply padding if enabled
		data, err := json.Marshal(forward)
		if err != nil {
			log.Printf("federation: failed to marshal forward: %v", err)
			continue
		}

		if pm.tarConfig.PaddingEnabled {
			data = PadPayload(data, pm.tarConfig.PadBuckets)
		}

		// Enqueue to batcher (or send directly if no batcher)
		if peer.Batcher != nil {
			peer.Batcher.Enqueue(data)
		} else {
			select {
			case peer.Send <- data:
			default:
				log.Printf("federation: peer %s send channel full, dropping", peer.ID)
				metrics.IncrementDropped()
			}
		}
	}
}

// GetHealthyPeers returns a list of healthy peer IDs.
func (pm *PeerManager) GetHealthyPeers() []string {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()

	var healthy []string
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

// GetPeerCount returns the current number of connected peers.
func (pm *PeerManager) GetPeerCount() int {
	pm.peersMu.RLock()
	defer pm.peersMu.RUnlock()
	return len(pm.peers)
}

// GetPeerMetrics returns peer metrics for all connected peers.
func (pm *PeerManager) GetPeerMetrics() []*model.PeerScoreResponse {
	pm.metricsMu.RLock()
	defer pm.metricsMu.RUnlock()

	var responses []*model.PeerScoreResponse
	for _, metrics := range pm.peerMetrics {
		responses = append(responses, metrics.ToResponse())
	}
	return responses
}

// IsPeerHealthy checks if a specific peer is healthy.
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

// RateLimiter implements per-peer rate limiting.
type RateLimiter struct {
	mu          sync.Mutex
	requests    map[string][]time.Time
	maxRequests int
	window      time.Duration
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		requests:    make(map[string][]time.Time),
		maxRequests: maxRequests,
		window:      window,
	}
}

// Allow checks if a request from the given peer is allowed.
func (rl *RateLimiter) Allow(peerID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.window)

	// Get or create the request history for this peer
	history, exists := rl.requests[peerID]
	if !exists {
		history = []time.Time{}
	}

	// Filter out old requests
	var validRequests []time.Time
	for _, t := range history {
		if t.After(windowStart) {
			validRequests = append(validRequests, t)
		}
	}

	// Check if under limit
	if len(validRequests) >= rl.maxRequests {
		rl.requests[peerID] = validRequests
		return false
	}

	// Add current request
	validRequests = append(validRequests, now)
	rl.requests[peerID] = validRequests
	return true
}

// Cleanup removes old entries from the rate limiter.
func (rl *RateLimiter) Cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.window)

	for peerID, history := range rl.requests {
		var validRequests []time.Time
		for _, t := range history {
			if t.After(windowStart) {
				validRequests = append(validRequests, t)
			}
		}
		if len(validRequests) == 0 {
			delete(rl.requests, peerID)
		} else {
			rl.requests[peerID] = validRequests
		}
	}
}

// diff returns elements in a that are not in b.
func diff(a, b []string) []string {
	set := make(map[string]bool)
	for _, v := range b {
		set[v] = true
	}
	var result []string
	for _, v := range a {
		if !set[v] {
			result = append(result, v)
		}
	}
	return result
}
