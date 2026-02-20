package model

import (
	"sync"
	"time"
)

// Message represents a persisted message in the store.
type Message struct {
	ID          string     `json:"msg_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Payload     string     `json:"payload_b64"` // Base64 encoded
	CreatedAt   time.Time  `json:"at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// QueueKey is the composite key for queue_by_recipient bucket.
type QueueKey struct {
	To        string
	CreatedAt time.Time
	MsgID     string
}

// DeliveryState represents the delivery status of a message.
type DeliveryState struct {
	MsgID       string     `json:"msg_id"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// WSFrame represents a WebSocket frame (protocol v1).
type WSFrame struct {
	Type       string    `json:"type"`
	WayfarerID string    `json:"wayfarer_id,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	TTLSeconds int       `json:"ttl_seconds,omitempty"`
	PayloadB64 string    `json:"payload_b64,omitempty"`
	MsgID      string    `json:"msg_id,omitempty"`
	Limit      int       `json:"limit,omitempty"`
	Messages   []Message `json:"messages,omitempty"`
	At         int64     `json:"at,omitempty"`
}

// Frame types (protocol v1)
const (
	FrameTypeHello    = "hello"
	FrameTypeHelloOK  = "hello_ok"
	FrameTypeSend     = "send"
	FrameTypeSendOK   = "send_ok"
	FrameTypeMessage  = "message"
	FrameTypeAck      = "ack"
	FrameTypeAckOK    = "ack_ok"
	FrameTypePull     = "pull"
	FrameTypeMessages = "messages"
	FrameTypeError    = "error"

	// Relay-to-relay federation frames (clients must never receive these)
	FrameTypeRelayHello     = "relay_hello"
	FrameTypeRelayInventory = "relay_inventory"
	FrameTypeRelayRequest   = "relay_request"
	FrameTypeRelayForward   = "relay_forward"
	FrameTypeRelayOK        = "relay_ok"
)

// RelayHelloFrame is the initial handshake between relays.
type RelayHelloFrame struct {
	Type    string `json:"type"`
	RelayID string `json:"relay_id"`
	Version string `json:"version"`
}

// RelayInventoryFrame carries message IDs known by a relay for a recipient.
type RelayInventoryFrame struct {
	Type        string   `json:"type"`
	RecipientID string   `json:"recipient_id"`
	MessageIDs  []string `json:"message_ids"`
}

// RelayRequestFrame requests full messages for given IDs.
type RelayRequestFrame struct {
	Type       string   `json:"type"`
	MessageIDs []string `json:"message_ids"`
}

// RelayForwardFrame carries a full message to be forwarded.
type RelayForwardFrame struct {
	Type    string   `json:"type"`
	Message *Message `json:"message"`
}

// Client represents a connected WebSocket client.
type Client struct {
	ID         string
	WayfarerID string
	Conn       interface {
		WriteJSON(v interface{}) error
		ReadJSON(v interface{}) error
		WriteMessage(msgType int, data []byte) error
		ReadMessage() (messageType int, p []byte, err error)
		Close() error
	}
	Send chan []byte
}

// ClientRegistry manages connected clients.
type ClientRegistry struct {
	mu         sync.RWMutex
	byID       map[string]map[*Client]bool // wayfarer_id -> set of clients
	byConn     map[*Client]string          // client -> wayfarer_id
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
}

func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{
		byID:       make(map[string]map[*Client]bool),
		byConn:     make(map[*Client]string),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
	}
}

func (r *ClientRegistry) Register(client *Client) {
	r.register <- client
}

func (r *ClientRegistry) Unregister(client *Client) {
	r.unregister <- client
}

func (r *ClientRegistry) Run() {
	for {
		select {
		case client := <-r.register:
			r.mu.Lock()
			if r.byID[client.WayfarerID] == nil {
				r.byID[client.WayfarerID] = make(map[*Client]bool)
			}
			r.byID[client.WayfarerID][client] = true
			r.byConn[client] = client.WayfarerID
			r.mu.Unlock()

		case client := <-r.unregister:
			r.mu.Lock()
			wayfarerID, ok := r.byConn[client]
			if !ok {
				r.mu.Unlock()
				continue
			}
			if clients, ok := r.byID[wayfarerID]; ok {
				if _, ok := clients[client]; ok {
					delete(clients, client)
					close(client.Send)
					if len(clients) == 0 {
						delete(r.byID, wayfarerID)
					}
				}
			}
			delete(r.byConn, client)
			r.mu.Unlock()
		}
	}
}

func (r *ClientRegistry) GetClients(wayfarerID string) []*Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var clients []*Client
	if s, ok := r.byID[wayfarerID]; ok {
		for c := range s {
			clients = append(clients, c)
		}
	}
	return clients
}

func (r *ClientRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byConn)
}

func (r *ClientRegistry) IsOnline(wayfarerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clients, ok := r.byID[wayfarerID]
	if !ok || len(clients) == 0 {
		return false
	}
	return true
}
