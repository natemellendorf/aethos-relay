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
)

// Peer represents a connected relay peer.
type Peer struct {
	ID            string
	URL           string
	Conn          *websocket.Conn
	Send          chan []byte
	LastInventory time.Time
	ConnectedAt   time.Time
	Health        PeerHealth
	Done          chan struct{}
}

// PeerHealth tracks peer health metrics.
type PeerHealth struct {
	Uptime            time.Duration
	LastSeen          time.Time
	MessagesForwarded int
	MessagesReceived  int
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
}

// NewPeerManager creates a new peer manager.
func NewPeerManager(relayID string, store store.Store, clients *model.ClientRegistry, maxTTL time.Duration) *PeerManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerManager{
		relayID:    relayID,
		peers:      make(map[string]*Peer),
		store:      store,
		clients:    clients,
		maxTTL:     maxTTL,
		ctx:        ctx,
		cancel:     cancel,
		inbound:    make(chan *Peer),
		outbound:   make(chan string),
		disconnect: make(chan *Peer),
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
		close(peer.Done)
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
		var helloResp map[string]interface{}
		if err := conn.ReadJSON(&helloResp); err != nil {
			log.Printf("federation: hello response failed %s: %v", url, err)
			conn.Close()
			time.Sleep(delay)
			delay = min(delay*2, ReconnectMaxDelay)
			continue
		}

		log.Printf("federation: connected to peer %s", url)

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

		peer.Health.LastSeen = time.Now()

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
	log.Printf("federation: peer added %s (total: %d)", peer.ID, len(pm.peers))
}

// removePeer removes a peer from the manager.
func (pm *PeerManager) removePeer(peer *Peer) {
	pm.peersMu.Lock()
	defer pm.peersMu.Unlock()
	delete(pm.peers, peer.ID)
	log.Printf("federation: peer removed %s (total: %d)", peer.ID, len(pm.peers))
}

// handleRelayHello handles incoming relay hello.
func (pm *PeerManager) handleRelayHello(peer *Peer, frame *model.RelayHelloFrame) {
	// Send hello_ok
	resp := map[string]string{
		"type":     model.FrameTypeRelayOK,
		"relay_id": pm.relayID,
	}
	peer.Conn.WriteJSON(resp)
}

// handleRelayInventory handles inventory gossip from peers.
func (pm *PeerManager) handleRelayInventory(peer *Peer, frame *model.RelayInventoryFrame) {
	recipientID := frame.RecipientID
	messageIDs := frame.MessageIDs

	if recipientID == "" || len(messageIDs) == 0 {
		return
	}

	peer.Health.LastSeen = time.Now()

	// Get local message IDs for this recipient
	localIDs, err := pm.getLocalMessageIDs(recipientID)
	if err != nil {
		log.Printf("federation: get local ids failed: %v", err)
		return
	}

	// Compute diff: what we have that peer doesn't
	missingFromPeer := diff(localIDs, messageIDs)

	if len(missingFromPeer) > 0 {
		// Request missing messages
		request := model.RelayRequestFrame{
			Type:       model.FrameTypeRelayRequest,
			MessageIDs: missingFromPeer,
		}
		data, _ := json.Marshal(request)
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
		data, _ := json.Marshal(forward)
		select {
		case peer.Send <- data:
			peer.Health.MessagesForwarded++
		default:
			log.Printf("federation: peer send channel full")
		}
	}
}

// handleRelayForward handles incoming forwarded messages.
func (pm *PeerManager) handleRelayForward(peer *Peer, frame *model.RelayForwardFrame) {
	msg := frame.Message
	if msg == nil || msg.ID == "" {
		return
	}

	peer.Health.MessagesReceived++

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

	// Gossip to other peers (propagate)
	go pm.gossipMessage(msg)
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
			pm.store.MarkDelivered(context.Background(), msg.ID)
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
			data, _ := json.Marshal(inv)
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
	data, _ := json.Marshal(inv)

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
		peer.Health.Uptime = time.Since(peer.ConnectedAt)
		result[id] = peer.Health
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

	// Send hello_ok
	resp := map[string]string{
		"type":     model.FrameTypeRelayOK,
		"relay_id": pm.relayID,
	}
	if err := conn.WriteJSON(resp); err != nil {
		conn.Close()
		return
	}

	log.Printf("federation: inbound peer connected from %s", r.RemoteAddr)

	// Start loops
	go pm.readLoop(peer)
	go pm.writeLoop(peer)

	pm.inbound <- peer
}

// AnnounceMessage announces a new message to federation.
func (pm *PeerManager) AnnounceMessage(msg *model.Message) {
	go pm.gossipMessage(msg)
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
