package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

type wsTestConn struct{}

func (c *wsTestConn) WriteJSON(v interface{}) error               { return nil }
func (c *wsTestConn) ReadJSON(v interface{}) error                { return nil }
func (c *wsTestConn) WriteMessage(msgType int, data []byte) error { return nil }
func (c *wsTestConn) ReadMessage() (int, []byte, error)           { return 0, nil, nil }
func (c *wsTestConn) Close() error                                { return nil }

type deliveredCall struct {
	msgID       string
	recipientID string
}

type wsStoreSpy struct {
	mu sync.Mutex

	queued            map[string][]*model.Message
	markDelivered     []deliveredCall
	isDeliveredChecks []deliveredCall
	getQueuedCalls    []string
	deliveredByMsg    map[string]map[string]bool
}

func newWSStoreSpy() *wsStoreSpy {
	return &wsStoreSpy{
		queued:         make(map[string][]*model.Message),
		deliveredByMsg: make(map[string]map[string]bool),
	}
}

func (s *wsStoreSpy) Open() error { return nil }

func (s *wsStoreSpy) Close() error { return nil }

func (s *wsStoreSpy) PersistMessage(ctx context.Context, msg *model.Message) error {
	return nil
}

func (s *wsStoreSpy) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getQueuedCalls = append(s.getQueuedCalls, recipientID)
	queued := s.queued[recipientID]
	if limit > 0 && len(queued) > limit {
		queued = queued[:limit]
	}
	result := make([]*model.Message, 0, len(queued))
	for _, msg := range queued {
		result = append(result, msg)
	}
	return result, nil
}

func (s *wsStoreSpy) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markDelivered = append(s.markDelivered, deliveredCall{msgID: msgID, recipientID: recipientID})
	if s.deliveredByMsg[msgID] == nil {
		s.deliveredByMsg[msgID] = make(map[string]bool)
	}
	s.deliveredByMsg[msgID][recipientID] = true
	return nil
}

func (s *wsStoreSpy) IsDeliveredTo(ctx context.Context, msgID string, recipientID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isDeliveredChecks = append(s.isDeliveredChecks, deliveredCall{msgID: msgID, recipientID: recipientID})
	return s.deliveredByMsg[msgID][recipientID], nil
}

func (s *wsStoreSpy) GetMessageByID(ctx context.Context, msgID string) (*model.Message, error) {
	return nil, nil
}

func (s *wsStoreSpy) RemoveMessage(ctx context.Context, msgID string) error {
	return nil
}

func (s *wsStoreSpy) GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error) {
	return nil, nil
}

func (s *wsStoreSpy) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (s *wsStoreSpy) SetLastSweepTime(ctx context.Context, t time.Time) error {
	return nil
}

func (s *wsStoreSpy) GetAllRecipientIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (s *wsStoreSpy) GetAllQueuedMessageIDs(ctx context.Context, to string) ([]string, error) {
	return nil, nil
}

func newWSHandlerWithSpyStore(t *testing.T) (*WSHandler, *wsStoreSpy) {
	t.Helper()
	st := newWSStoreSpy()
	clients := model.NewClientRegistry()
	go clients.Run()
	h := NewWSHandler(st, clients, time.Hour, "", true)
	h.SetAutoDeliverQueued(false)
	return h, st
}

func newClientForWSHandler() *model.Client {
	return &model.Client{
		Conn:                &wsTestConn{},
		Send:                make(chan []byte, 16),
		PayloadEncodingPref: model.PayloadEncodingPrefBase64,
	}
}

func readQueuedFrame(t *testing.T, c *model.Client) model.WSFrame {
	t.Helper()
	payload := readQueuedPayload(t, c)
	var frame model.WSFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("decode queued frame: %v", err)
	}
	return frame
}

func readQueuedPayload(t *testing.T, c *model.Client) []byte {
	t.Helper()
	select {
	case payload := <-c.Send:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued frame")
	}
	return nil
}

func decodeFrameMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("decode queued frame map: %v", err)
	}
	return frame
}

func TestHandleHelloAllowsOptionalDeviceID(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()

	h.handleHello(client, &model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-a", DeviceID: "device-1"})

	if client.WayfarerID != "wayfarer-a" {
		t.Fatalf("wayfarer mismatch: got %q", client.WayfarerID)
	}
	if client.DeviceID != "device-1" {
		t.Fatalf("device mismatch: got %q", client.DeviceID)
	}
	wantDeliveryID := storeforward.DeliveryIdentity("wayfarer-a", "device-1")
	if client.DeliveryID != wantDeliveryID {
		t.Fatalf("delivery identity mismatch: got %q want %q", client.DeliveryID, wantDeliveryID)
	}

	resp := readQueuedFrame(t, client)
	if resp.Type != model.FrameTypeHelloOK {
		t.Fatalf("unexpected frame type: got %q want %q", resp.Type, model.FrameTypeHelloOK)
	}
}

func TestHandleHelloLegacyFallbackUsesWayfarerIdentity(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()

	h.handleHello(client, &model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-a"})

	if client.DeviceID != "" {
		t.Fatalf("expected empty device id, got %q", client.DeviceID)
	}
	if client.DeliveryID != "wayfarer-a" {
		t.Fatalf("legacy delivery identity mismatch: got %q want %q", client.DeliveryID, "wayfarer-a")
	}
}

func TestHandlePullAndAckUseDeviceDeliveryIdentity(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	st.queued[client.WayfarerID] = []*model.Message{{
		ID:        "msg-device",
		From:      "wayfarer-a",
		To:        client.WayfarerID,
		Payload:   "QQ==",
		CreatedAt: time.Now(),
	}}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-device"})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.getQueuedCalls) == 0 || st.getQueuedCalls[0] != client.WayfarerID {
		t.Fatalf("pull should read queue for wayfarer bucket, calls=%v", st.getQueuedCalls)
	}
	if len(st.isDeliveredChecks) == 0 || st.isDeliveredChecks[0].recipientID != client.DeliveryID {
		t.Fatalf("pull should filter by device delivery identity, checks=%v", st.isDeliveredChecks)
	}
	if len(st.markDelivered) == 0 || st.markDelivered[len(st.markDelivered)-1].recipientID != client.DeliveryID {
		t.Fatalf("ack should mark delivered for tracked device identity, calls=%v", st.markDelivered)
	}
}

func TestHandleAckLegacyFallbackUsesWayfarerIdentity(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-legacy"})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.markDelivered) != 1 {
		t.Fatalf("expected one ack delivery write, got %d", len(st.markDelivered))
	}
	if st.markDelivered[0].recipientID != client.WayfarerID {
		t.Fatalf("fallback should use wayfarer identity, got %q want %q", st.markDelivered[0].recipientID, client.WayfarerID)
	}
}

func TestHandleSendSendOKIncludesCanonicalAndLegacyTimestamps(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	sender := newClientForWSHandler()
	sender.WayfarerID = "wayfarer-a"

	h.handleSend(sender, &model.WSFrame{
		Type:       model.FrameTypeSend,
		To:         "wayfarer-b",
		PayloadB64: "QQ==",
		TTLSeconds: 120,
	})

	payload := readQueuedPayload(t, sender)
	decoded := decodeFrameMap(t, payload)

	if decoded["type"] != model.FrameTypeSendOK {
		t.Fatalf("expected send_ok frame, got %v", decoded["type"])
	}
	at, ok := decoded["at"].(float64)
	if !ok {
		t.Fatalf("expected numeric at field, got %T", decoded["at"])
	}
	receivedAt, ok := decoded["received_at"].(float64)
	if !ok {
		t.Fatalf("expected numeric received_at field, got %T", decoded["received_at"])
	}
	expiresAt, ok := decoded["expires_at"].(float64)
	if !ok {
		t.Fatalf("expected numeric expires_at field, got %T", decoded["expires_at"])
	}
	if at != receivedAt {
		t.Fatalf("expected at and received_at to match, got at=%v received_at=%v", at, receivedAt)
	}
	if expiresAt <= receivedAt {
		t.Fatalf("expected expires_at > received_at, got expires_at=%v received_at=%v", expiresAt, receivedAt)
	}
}

func TestHandleSendRejectsInvalidPayloadB64(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	sender := newClientForWSHandler()
	sender.WayfarerID = "wayfarer-a"

	h.handleSend(sender, &model.WSFrame{
		Type:       model.FrameTypeSend,
		To:         "wayfarer-b",
		PayloadB64: "%%%",
		TTLSeconds: 120,
	})

	resp := readQueuedFrame(t, sender)
	if resp.Type != model.FrameTypeError {
		t.Fatalf("expected error frame, got %q", resp.Type)
	}
	if resp.MsgID != "invalid payload_b64" {
		t.Fatalf("unexpected error payload: got %q", resp.MsgID)
	}
}

func TestHandleSendInfersPayloadEncodingPreference(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    model.PayloadEncodingPref
	}{
		{name: "dash implies base64url", payload: "-_8", want: model.PayloadEncodingPrefBase64URL},
		{name: "plus implies base64", payload: "+/8=", want: model.PayloadEncodingPrefBase64},
		{name: "unpadded length implies base64url", payload: "Zg", want: model.PayloadEncodingPrefBase64URL},
		{name: "ambiguous padded defaults base64", payload: "Zm8=", want: model.PayloadEncodingPrefBase64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newWSHandlerWithSpyStore(t)
			sender := newClientForWSHandler()
			sender.WayfarerID = "wayfarer-a"

			h.handleSend(sender, &model.WSFrame{
				Type:       model.FrameTypeSend,
				To:         "wayfarer-b",
				PayloadB64: tt.payload,
				TTLSeconds: 120,
			})

			resp := readQueuedFrame(t, sender)
			if resp.Type != model.FrameTypeSendOK {
				t.Fatalf("expected send_ok frame, got %q", resp.Type)
			}
			if sender.PayloadEncodingPref != tt.want {
				t.Fatalf("preference mismatch: got %q want %q", sender.PayloadEncodingPref, tt.want)
			}
		})
	}
}

func TestDeliverToRecipientIncludesCanonicalAndLegacyMessageTimestamps(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	recipient := newClientForWSHandler()
	recipient.WayfarerID = "wayfarer-b"
	recipient.DeliveryID = "wayfarer-b"
	h.clients.Register(recipient)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.clients.Count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.clients.Count() == 0 {
		t.Fatal("expected recipient to be registered")
	}

	createdAt := time.Unix(1_700_000_123, 0).UTC()
	h.deliverToRecipient(&model.Message{
		ID:        "msg-1",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "QQ==",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	})

	payload := readQueuedPayload(t, recipient)
	decoded := decodeFrameMap(t, payload)

	if decoded["type"] != model.FrameTypeMessage {
		t.Fatalf("expected message frame, got %v", decoded["type"])
	}
	at, ok := decoded["at"].(float64)
	if !ok {
		t.Fatalf("expected numeric at field, got %T", decoded["at"])
	}
	receivedAt, ok := decoded["received_at"].(float64)
	if !ok {
		t.Fatalf("expected numeric received_at field, got %T", decoded["received_at"])
	}
	if int64(at) != createdAt.Unix() || int64(receivedAt) != createdAt.Unix() {
		t.Fatalf("unexpected timestamps, got at=%v received_at=%v want=%d", at, receivedAt, createdAt.Unix())
	}
	if _, ok := decoded["expires_at"]; ok {
		t.Fatal("message push should not include expires_at")
	}
}

func TestDeliverToRecipientEncodesPayloadByRecipientPreference(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	recipientURL := newClientForWSHandler()
	recipientURL.WayfarerID = "wayfarer-b"
	recipientURL.DeliveryID = "wayfarer-b"
	recipientURL.PayloadEncodingPref = model.PayloadEncodingPrefBase64URL
	h.clients.Register(recipientURL)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.clients.Count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.clients.Count() == 0 {
		t.Fatal("expected recipient to be registered")
	}

	createdAt := time.Unix(1_700_000_789, 0).UTC()
	h.deliverToRecipient(&model.Message{
		ID:        "msg-url-pref",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "+/8=",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	})

	frame := readQueuedFrame(t, recipientURL)
	if frame.Type != model.FrameTypeMessage {
		t.Fatalf("expected message frame, got %q", frame.Type)
	}
	if frame.PayloadB64 != "-_8" {
		t.Fatalf("expected base64url payload, got %q", frame.PayloadB64)
	}
	if strings.Contains(frame.PayloadB64, "=") {
		t.Fatalf("base64url payload should be unpadded, got %q", frame.PayloadB64)
	}
	decoded, err := model.DecodePayloadB64(frame.PayloadB64)
	if err != nil {
		t.Fatalf("decode delivered payload: %v", err)
	}
	if base64.StdEncoding.EncodeToString(decoded) != "+/8=" {
		t.Fatalf("round-trip payload mismatch: got %q", base64.StdEncoding.EncodeToString(decoded))
	}
}

func TestHandlePullEncodesPayloadByRecipientPreference(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeliveryID = "wayfarer-b"
	client.PayloadEncodingPref = model.PayloadEncodingPrefBase64URL

	createdAt := time.Unix(1_700_000_900, 0).UTC()
	st.queued[client.WayfarerID] = []*model.Message{{
		ID:        "msg-pull-url-pref",
		From:      "wayfarer-a",
		To:        client.WayfarerID,
		Payload:   "+/8=",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})

	frame := readQueuedFrame(t, client)
	if frame.Type != model.FrameTypeMessages {
		t.Fatalf("expected messages frame, got %q", frame.Type)
	}
	if len(frame.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(frame.Messages))
	}
	if frame.Messages[0].Payload != "-_8" {
		t.Fatalf("expected base64url payload in pull response, got %q", frame.Messages[0].Payload)
	}
	if strings.Contains(frame.Messages[0].Payload, "=") {
		t.Fatalf("expected unpadded base64url payload in pull response, got %q", frame.Messages[0].Payload)
	}
	decoded, err := model.DecodePayloadB64(frame.Messages[0].Payload)
	if err != nil {
		t.Fatalf("decode pulled payload: %v", err)
	}
	if base64.StdEncoding.EncodeToString(decoded) != "+/8=" {
		t.Fatalf("round-trip pulled payload mismatch: got %q", base64.StdEncoding.EncodeToString(decoded))
	}
}

func TestDeliverToRecipientDefaultsToLegacyBase64Padding(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	recipient := newClientForWSHandler()
	recipient.WayfarerID = "wayfarer-b"
	recipient.DeliveryID = "wayfarer-b"
	recipient.PayloadEncodingPref = model.PayloadEncodingPrefBase64
	h.clients.Register(recipient)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.clients.Count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.clients.Count() == 0 {
		t.Fatal("expected recipient to be registered")
	}

	createdAt := time.Unix(1_700_001_000, 0).UTC()
	h.deliverToRecipient(&model.Message{
		ID:        "msg-std-pref",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "-_8",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	})

	frame := readQueuedFrame(t, recipient)
	if frame.PayloadB64 != "+/8=" {
		t.Fatalf("expected standard base64 payload, got %q", frame.PayloadB64)
	}
	if !strings.Contains(frame.PayloadB64, "=") {
		t.Fatalf("expected padded standard base64 payload, got %q", frame.PayloadB64)
	}
}

func TestHandlePullMessagesKeepLegacyAndAddCanonicalTimestamps(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeliveryID = "wayfarer-b"

	createdAt := time.Unix(1_700_000_456, 0).UTC()
	st.queued[client.WayfarerID] = []*model.Message{{
		ID:        "msg-pull-1",
		From:      "wayfarer-a",
		To:        client.WayfarerID,
		Payload:   "QQ==",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})

	payload := readQueuedPayload(t, client)
	decoded := decodeFrameMap(t, payload)

	if decoded["type"] != model.FrameTypeMessages {
		t.Fatalf("expected messages frame, got %v", decoded["type"])
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one pulled message, got %#v", decoded["messages"])
	}
	entry, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected message entry object, got %T", messages[0])
	}
	at, ok := entry["at"].(string)
	if !ok {
		t.Fatalf("expected string at field, got %T", entry["at"])
	}
	receivedAt, ok := entry["received_at"].(float64)
	if !ok {
		t.Fatalf("expected numeric received_at field, got %T", entry["received_at"])
	}
	if at != createdAt.Format(time.RFC3339Nano) || int64(receivedAt) != createdAt.Unix() {
		t.Fatalf("unexpected pulled timestamps, got at=%v received_at=%v want=%d", at, receivedAt, createdAt.Unix())
	}
	if expiresAt, ok := entry["expires_at"].(string); !ok || expiresAt == "" {
		t.Fatalf("expected legacy expires_at string field, got %#v", entry["expires_at"])
	}
	if delivered, ok := entry["delivered"].(bool); !ok || delivered {
		t.Fatalf("expected legacy delivered=false field, got %#v", entry["delivered"])
	}
}
