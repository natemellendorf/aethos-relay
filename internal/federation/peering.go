package federation

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

const (
	HeartbeatInterval = 30 * time.Second

	ReconnectBaseDelay = 1 * time.Second
	ReconnectMaxDelay  = 60 * time.Second

	ProtocolDiagnosticsInterval = 10 * time.Second
)

var ErrFederationPropagationDisabled = errors.New("federation propagation disabled in Gossip V1 Phase 1")

type Peer struct {
	ID            string
	URL           string
	Outbound      bool
	Conn          *websocket.Conn
	ConnectedAt   time.Time
	LastHelloAt   time.Time
	Health        PeerHealth
	healthMu      sync.Mutex
	Done          chan struct{}
	doneOnce      sync.Once
	adapter       *gossipv1.SessionAdapter
	adapterMu     sync.Mutex
	LastMalformed time.Time
	Metrics       *model.PeerMetrics
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

	return &PeerManager{
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
		peerMetrics:      make(map[string]*model.PeerMetrics),
	}
}

func (pm *PeerManager) SetEnvelopeStore(envelopeStore store.EnvelopeStore) {
	_ = envelopeStore
}

func (pm *PeerManager) SetAckDrivenSuppression(enabled bool) {
	_ = enabled
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
	if err := peer.Conn.WriteMessage(websocket.BinaryMessage, helloBytes); err != nil {
		return fmt.Errorf("send HELLO: %w", err)
	}

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
			peer.healthMu.Lock()
			peer.Health.LastSeen = time.Now()
			peer.Health.IsHealthy = true
			peer.LastHelloAt = time.Now()
			peer.healthMu.Unlock()
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
			continue
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
			if err := peer.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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
	if err := pm.processEvents(peer, adapter, events); err != nil {
		return err
	}
	if !adapter.IsHealthy() {
		return errors.New("gossip session unhealthy after inbound HELLO")
	}

	helloBytes, err := adapter.InitialHelloBytes()
	if err != nil {
		return err
	}
	if err := peer.Conn.WriteMessage(websocket.BinaryMessage, helloBytes); err != nil {
		return err
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
		return fmt.Errorf("%w: announce attempted with nil message", ErrFederationPropagationDisabled)
	}

	return fmt.Errorf("%w: announce message id=%s", ErrFederationPropagationDisabled, msg.ID)
}

func (pm *PeerManager) ForwardToPeers(msg *model.Message, originRelayID string) error {
	if msg == nil {
		return fmt.Errorf("%w: forward attempted with nil message", ErrFederationPropagationDisabled)
	}

	if originRelayID == "" {
		return fmt.Errorf("%w: forward message id=%s with empty origin relay id", ErrFederationPropagationDisabled, msg.ID)
	}

	return fmt.Errorf("%w: forward message id=%s origin=%s", ErrFederationPropagationDisabled, msg.ID, originRelayID)
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
