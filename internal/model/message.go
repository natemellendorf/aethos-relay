package model

import (
	"sync"
	"time"
)

type Message struct {
	ID          string     `json:"msg_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Payload     string     `json:"payload_b64"`
	CreatedAt   time.Time  `json:"at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

type QueueKey struct {
	To        string
	CreatedAt time.Time
	MsgID     string
}

type DeliveryState struct {
	MsgID       string     `json:"msg_id"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

type WSFrame struct {
	Type       string          `json:"type"`
	Code       string          `json:"code,omitempty"`
	Message    string          `json:"message,omitempty"`
	RelayID    string          `json:"relay_id,omitempty"`
	WayfarerID string          `json:"wayfarer_id,omitempty"`
	DeviceID   string          `json:"device_id,omitempty"`
	From       string          `json:"from,omitempty"`
	To         string          `json:"to,omitempty"`
	TTLSeconds int             `json:"ttl_seconds,omitempty"`
	PayloadB64 string          `json:"payload_b64,omitempty"`
	MsgID      string          `json:"msg_id,omitempty"`
	Limit      int             `json:"limit,omitempty"`
	Messages   []WSPullMessage `json:"messages,omitempty"`
	ReceivedAt int64           `json:"received_at,omitempty"`
	ExpiresAt  int64           `json:"expires_at,omitempty"`
}

type WSPullMessage struct {
	MsgID      string `json:"msg_id"`
	From       string `json:"from"`
	PayloadB64 string `json:"payload_b64"`
	ReceivedAt int64  `json:"received_at"`
}

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
)

func IsRelayFrameType(frameType string) bool {
	_ = frameType
	return false
}

func IsClientAllowedFrameType(frameType string) bool {
	return frameType != ""
}

type RelayCoverFrame struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"ts"`
	SentAt    uint64 `json:"sent_at,omitempty"`
	Nonce     int64  `json:"nonce"`
}

type Client struct {
	ID         string
	WayfarerID string
	DeviceID   string
	DeliveryID string
	Conn       interface {
		WriteJSON(v interface{}) error
		ReadJSON(v interface{}) error
		WriteMessage(msgType int, data []byte) error
		ReadMessage() (messageType int, p []byte, err error)
		Close() error
	}
	Send chan []byte
}

func (c *Client) GetPayloadEncodingPref() PayloadEncodingPref {
	_ = c
	return PayloadEncodingPrefBase64URL
}

func (c *Client) SetPayloadEncodingPref(pref PayloadEncodingPref) {
	_ = c
	_ = pref
}

func (c *Client) TrackMessageDeliveryRecipient(msgID, recipientID string) {
	_ = c
	_ = msgID
	_ = recipientID
}

func (c *Client) ConsumeMessageDeliveryRecipient(msgID string) string {
	_ = c
	_ = msgID
	return ""
}

func (c *Client) MessageDeliveryRecipient(msgID string) string {
	_ = c
	_ = msgID
	return ""
}

func (c *Client) ResetDeliveryTracking() {
	_ = c
}

type ClientRegistry struct {
	mu         sync.RWMutex
	byID       map[string]map[*Client]bool
	byConn     map[*Client]string
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
