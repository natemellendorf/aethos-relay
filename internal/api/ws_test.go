package api

import (
	"context"
	"encoding/json"
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

type ackCall struct {
	msgID       string
	recipientID string
}

type wsStoreSpy struct {
	queued      map[string][]*model.Message
	persisted   []*model.Message
	ackedByMsg  map[string]map[string]bool
	markAcked   []ackCall
	persistErr  error
	getQueueErr error
	ackErr      error
}

func newWSStoreSpy() *wsStoreSpy {
	return &wsStoreSpy{
		queued:     make(map[string][]*model.Message),
		ackedByMsg: make(map[string]map[string]bool),
	}
}

func (s *wsStoreSpy) Open() error  { return nil }
func (s *wsStoreSpy) Close() error { return nil }
func (s *wsStoreSpy) PersistMessage(ctx context.Context, msg *model.Message) error {
	if s.persistErr != nil {
		return s.persistErr
	}
	cp := *msg
	s.persisted = append(s.persisted, &cp)
	return nil
}
func (s *wsStoreSpy) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	return s.GetQueuedMessagesRaw(ctx, recipientID, limit)
}
func (s *wsStoreSpy) GetQueuedMessagesRaw(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	if s.getQueueErr != nil {
		return nil, s.getQueueErr
	}
	queued := s.queued[recipientID]
	if limit > 0 && len(queued) > limit {
		queued = queued[:limit]
	}
	return queued, nil
}
func (s *wsStoreSpy) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	return nil
}
func (s *wsStoreSpy) MarkAcked(ctx context.Context, msgID string, recipientID string) (bool, error) {
	if s.ackErr != nil {
		return false, s.ackErr
	}
	s.markAcked = append(s.markAcked, ackCall{msgID: msgID, recipientID: recipientID})
	if s.ackedByMsg[msgID] == nil {
		s.ackedByMsg[msgID] = make(map[string]bool)
	}
	if s.ackedByMsg[msgID][recipientID] {
		return false, nil
	}
	s.ackedByMsg[msgID][recipientID] = true
	return true, nil
}
func (s *wsStoreSpy) IsDeliveredTo(ctx context.Context, msgID string, recipientID string) (bool, error) {
	return false, nil
}
func (s *wsStoreSpy) IsAckedBy(ctx context.Context, msgID string, recipientID string) (bool, error) {
	return s.ackedByMsg[msgID][recipientID], nil
}
func (s *wsStoreSpy) GetMessageByID(ctx context.Context, msgID string) (*model.Message, error) {
	return nil, nil
}
func (s *wsStoreSpy) RemoveMessage(ctx context.Context, msgID string) error { return nil }
func (s *wsStoreSpy) GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error) {
	return nil, nil
}
func (s *wsStoreSpy) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (s *wsStoreSpy) SetLastSweepTime(ctx context.Context, t time.Time) error  { return nil }
func (s *wsStoreSpy) GetAllRecipientIDs(ctx context.Context) ([]string, error) { return nil, nil }
func (s *wsStoreSpy) GetAllQueuedMessageIDs(ctx context.Context, to string) ([]string, error) {
	return nil, nil
}

func newWSHandlerWithSpyStore(t *testing.T) (*WSHandler, *wsStoreSpy) {
	t.Helper()
	st := newWSStoreSpy()
	clients := model.NewClientRegistry()
	go clients.Run()
	h := NewWSHandler(st, clients, time.Hour, "", true, "relay-test")
	h.SetAutoDeliverQueued(false)
	return h, st
}

func newClientForWSHandler() *model.Client {
	return &model.Client{Conn: &wsTestConn{}, Send: make(chan []byte, 16)}
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

func TestHandleHelloRequiresDeviceID(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()

	h.handleHello(client, &model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-a"})

	frame := decodeFrameMap(t, readQueuedPayload(t, client))
	if frame["type"] != model.FrameTypeError {
		t.Fatalf("expected error frame, got %v", frame["type"])
	}
	if frame["message"] != "device_id required" {
		t.Fatalf("expected device_id required, got %v", frame["message"])
	}
	if _, ok := frame["msg_id"]; ok {
		t.Fatalf("unexpected legacy msg_id field: %v", frame)
	}
}

func TestHandleHelloOKIncludesRelayID(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()

	h.handleHello(client, &model.WSFrame{
		Type:       model.FrameTypeHello,
		WayfarerID: "wayfarer-a",
		DeviceID:   "device-a",
	})

	frame := decodeFrameMap(t, readQueuedPayload(t, client))
	if frame["type"] != model.FrameTypeHelloOK {
		t.Fatalf("expected hello_ok frame, got %v", frame["type"])
	}
	if frame["relay_id"] != "relay-test" {
		t.Fatalf("expected relay_id relay-test, got %v", frame["relay_id"])
	}
}

func TestHandleSendRejectsNonCanonicalPayloadB64(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	for _, payload := range []string{"QQ==", " \n\tQQ\r "} {
		client := newClientForWSHandler()
		client.WayfarerID = "wayfarer-a"

		h.handleSend(client, &model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b", PayloadB64: payload})

		frame := decodeFrameMap(t, readQueuedPayload(t, client))
		if frame["type"] != model.FrameTypeError || frame["message"] != "invalid payload_b64" {
			t.Fatalf("expected invalid payload error for %q, got %#v", payload, frame)
		}
	}
}

func TestHandleSendSendOKCanonicalFieldsOnly(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-a"

	h.handleSend(client, &model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b", PayloadB64: "QQ", TTLSeconds: 120})

	frame := decodeFrameMap(t, readQueuedPayload(t, client))
	if frame["type"] != model.FrameTypeSendOK {
		t.Fatalf("expected send_ok, got %#v", frame)
	}
	if _, ok := frame["at"]; ok {
		t.Fatalf("send_ok must not include legacy at alias: %#v", frame)
	}
	if _, ok := frame["received_at"]; !ok {
		t.Fatalf("send_ok missing received_at: %#v", frame)
	}
	if _, ok := frame["expires_at"]; !ok {
		t.Fatalf("send_ok missing expires_at: %#v", frame)
	}
	if len(st.persisted) != 1 || st.persisted[0].Payload != "QQ" {
		t.Fatalf("expected persisted canonical payload QQ, got %#v", st.persisted)
	}
}

func TestHandlePullEmitsCanonicalMessageEntry(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	st.queued[client.WayfarerID] = []*model.Message{{
		ID:        "msg-1",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "QQ",
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_700_000_000, 0).UTC().Add(time.Hour),
	}}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})

	frame := decodeFrameMap(t, readQueuedPayload(t, client))
	messages := frame["messages"].([]any)
	entry := messages[0].(map[string]any)
	if _, ok := entry["msg_id"]; !ok {
		t.Fatalf("missing msg_id: %#v", entry)
	}
	if _, ok := entry["payload_b64"]; !ok {
		t.Fatalf("missing payload_b64: %#v", entry)
	}
	if _, ok := entry["received_at"]; !ok {
		t.Fatalf("missing received_at: %#v", entry)
	}
	if _, ok := entry["at"]; ok {
		t.Fatalf("unexpected legacy at field: %#v", entry)
	}
	if _, ok := entry["expires_at"]; ok {
		t.Fatalf("unexpected legacy expires_at field: %#v", entry)
	}
}

func TestHandleAckUsesConnectionDeviceIdentity(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-1"})

	if len(st.markAcked) != 1 {
		t.Fatalf("expected one mark ack call, got %d", len(st.markAcked))
	}
	if st.markAcked[0].recipientID != storeforward.DeliveryIdentity("wayfarer-b", "device-a") {
		t.Fatalf("ack recipient mismatch: %#v", st.markAcked[0])
	}
}
