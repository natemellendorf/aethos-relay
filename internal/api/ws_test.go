package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
	persisted         []*model.Message
	removedMsgIDs     []string
	markDelivered     []deliveredCall
	isDeliveredChecks []deliveredCall
	getQueuedCalls    []string
	deliveredByMsg    map[string]map[string]bool
	ackedByMsg        map[string]map[string]bool

	persistErr       error
	markDeliveredErr error
	markAckedErr     error
	getQueuedErr     error
	isDeliveredErr   error
	isAckedErr       error
}

func newWSStoreSpy() *wsStoreSpy {
	return &wsStoreSpy{
		queued:         make(map[string][]*model.Message),
		deliveredByMsg: make(map[string]map[string]bool),
		ackedByMsg:     make(map[string]map[string]bool),
	}
}

func (s *wsStoreSpy) Open() error { return nil }

func (s *wsStoreSpy) Close() error { return nil }

func (s *wsStoreSpy) PersistMessage(ctx context.Context, msg *model.Message) error {
	if s.persistErr != nil {
		return s.persistErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copyMsg := *msg
	s.persisted = append(s.persisted, &copyMsg)
	return nil
}

func (s *wsStoreSpy) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	if s.getQueuedErr != nil {
		return nil, s.getQueuedErr
	}
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

func (s *wsStoreSpy) GetQueuedMessagesRaw(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	return s.GetQueuedMessages(ctx, recipientID, limit)
}

func (s *wsStoreSpy) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	if s.markDeliveredErr != nil {
		return s.markDeliveredErr
	}
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
	if s.isDeliveredErr != nil {
		return false, s.isDeliveredErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isDeliveredChecks = append(s.isDeliveredChecks, deliveredCall{msgID: msgID, recipientID: recipientID})
	return s.deliveredByMsg[msgID][recipientID], nil
}

func (s *wsStoreSpy) MarkAcked(ctx context.Context, msgID string, recipientID string) (bool, error) {
	if s.markAckedErr != nil {
		return false, s.markAckedErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ackedByMsg[msgID] == nil {
		s.ackedByMsg[msgID] = make(map[string]bool)
	}
	if s.ackedByMsg[msgID][recipientID] {
		return false, nil
	}
	s.ackedByMsg[msgID][recipientID] = true
	return true, nil
}

func (s *wsStoreSpy) IsAckedBy(ctx context.Context, msgID string, recipientID string) (bool, error) {
	if s.isAckedErr != nil {
		return false, s.isAckedErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ackedByMsg[msgID][recipientID], nil
}

func (s *wsStoreSpy) GetMessageByID(ctx context.Context, msgID string) (*model.Message, error) {
	return nil, nil
}

func (s *wsStoreSpy) RemoveMessage(ctx context.Context, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removedMsgIDs = append(s.removedMsgIDs, msgID)
	for recipientID, queued := range s.queued {
		filtered := make([]*model.Message, 0, len(queued))
		for _, msg := range queued {
			if msg == nil || msg.ID != msgID {
				filtered = append(filtered, msg)
			}
		}
		if len(filtered) == 0 {
			delete(s.queued, recipientID)
			continue
		}
		s.queued[recipientID] = filtered
	}
	delete(s.deliveredByMsg, msgID)
	delete(s.ackedByMsg, msgID)
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
	client := &model.Client{
		Conn: &wsTestConn{},
		Send: make(chan []byte, 16),
	}
	client.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64)
	return client
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

func readCounterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := counter.Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if m.Counter == nil {
		t.Fatal("counter metric is nil")
	}
	return m.Counter.GetValue()
}

func requireCounterDelta(t *testing.T, before, after, wantDelta float64, name string) {
	t.Helper()
	delta := after - before
	if math.Abs(delta-wantDelta) > 1e-9 {
		t.Fatalf("%s delta mismatch: got %.0f want %.0f", name, delta, wantDelta)
	}
}

func assertErrorFrameCompat(t *testing.T, frame model.WSFrame, wantCode model.ErrorCode, wantMessage string) {
	t.Helper()
	if frame.Type != model.FrameTypeError {
		t.Fatalf("expected error frame, got %q", frame.Type)
	}
	if frame.Code != string(wantCode) {
		t.Fatalf("error code mismatch: got %q want %q", frame.Code, wantCode)
	}
	if frame.Message != wantMessage {
		t.Fatalf("error message mismatch: got %q want %q", frame.Message, wantMessage)
	}
	if frame.MsgID != wantMessage {
		t.Fatalf("legacy msg_id mismatch: got %q want %q", frame.MsgID, wantMessage)
	}
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
		t.Fatalf("ack should mark delivered for connection device identity, calls=%v", st.markDelivered)
	}
}

func TestHandlePullIsDeliveredToFailureMapsToInternalError(t *testing.T) {
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
	st.isDeliveredErr = errors.New("is delivered failed")

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})

	resp := readQueuedFrame(t, client)
	assertErrorFrameCompat(t, resp, model.ErrorCodeInternalError, "failed to pull messages")
}

func TestHandleAckWithoutTrackingFallsBackToConnectionDeliveryIdentity(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)
	client.ResetDeliveryTracking()

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-legacy"})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.markDelivered) != 1 {
		t.Fatalf("expected one ack delivery write, got %d", len(st.markDelivered))
	}
	if st.markDelivered[0].recipientID != client.DeliveryID {
		t.Fatalf("ack should use connection delivery identity fallback, got %q want %q", st.markDelivered[0].recipientID, client.DeliveryID)
	}
}

func TestHandleAckFallsBackToWayfarerWhenDeliveryIdentityEmpty(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-wayfarer"})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.markDelivered) != 1 {
		t.Fatalf("expected one ack delivery write, got %d", len(st.markDelivered))
	}
	if st.markDelivered[0].recipientID != client.WayfarerID {
		t.Fatalf("fallback should use wayfarer identity, got %q want %q", st.markDelivered[0].recipientID, client.WayfarerID)
	}
}

func TestHandleAckOKShapeStable(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-ack-shape"})

	payload := readQueuedPayload(t, client)
	decoded := decodeFrameMap(t, payload)
	if decoded["type"] != model.FrameTypeAckOK {
		t.Fatalf("expected ack_ok type, got %v", decoded["type"])
	}
	if decoded["msg_id"] != "msg-ack-shape" {
		t.Fatalf("expected ack_ok msg_id, got %v", decoded["msg_id"])
	}
	if len(decoded) != 2 {
		t.Fatalf("ack_ok should only include type and msg_id, got %v", decoded)
	}
}

func TestHandleAckCanonicalModeIsIdempotent(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	h.SetAckDrivenSuppression(true)

	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-idempotent"})
	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-idempotent"})

	first := readQueuedFrame(t, client)
	second := readQueuedFrame(t, client)
	if first.Type != model.FrameTypeAckOK || second.Type != model.FrameTypeAckOK {
		t.Fatalf("expected ack_ok responses, got %q and %q", first.Type, second.Type)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.markDelivered) != 0 {
		t.Fatalf("canonical mode should not write legacy delivery state on ack, got %v", st.markDelivered)
	}
	if !st.ackedByMsg["msg-idempotent"][client.DeliveryID] {
		t.Fatalf("canonical mode should persist ack state for delivery identity fallback %q", client.DeliveryID)
	}
}

func TestLegacyModeDeliveryMetricsPushOnlyAndAckNoIncrementAfterPush(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)

	recipient := newClientForWSHandler()
	recipient.WayfarerID = "wayfarer-b"
	recipient.DeviceID = "device-a"
	recipient.DeliveryID = storeforward.DeliveryIdentity(recipient.WayfarerID, recipient.DeviceID)
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

	beforePush := readCounterValue(t, metrics.MessagesDeliveredTotal)

	createdAt := time.Unix(1_700_010_000, 0).UTC()
	msg := &model.Message{
		ID:        "msg-legacy-metric",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "QQ==",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}
	h.deliverToRecipient(msg)
	_ = readQueuedFrame(t, recipient)

	afterPush := readCounterValue(t, metrics.MessagesDeliveredTotal)
	requireCounterDelta(t, beforePush, afterPush, 1, "legacy push delivered")

	st.mu.Lock()
	if len(st.markDelivered) != 1 {
		st.mu.Unlock()
		t.Fatalf("expected exactly one legacy MarkDelivered call, got %d", len(st.markDelivered))
	}
	st.mu.Unlock()

	h.handleAck(recipient, &model.WSFrame{Type: model.FrameTypeAck, MsgID: msg.ID})
	resp := readQueuedFrame(t, recipient)
	if resp.Type != model.FrameTypeAckOK {
		t.Fatalf("expected ack_ok, got %q", resp.Type)
	}

	afterAck := readCounterValue(t, metrics.MessagesDeliveredTotal)
	requireCounterDelta(t, afterPush, afterAck, 0, "legacy ack after push delivered")
}

func TestAckDrivenModeMetricsIncrementOnAckTransitionOnly(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	h.SetAckDrivenSuppression(true)

	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeviceID = "device-a"
	client.DeliveryID = storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)

	beforeAck := readCounterValue(t, metrics.MessagesDeliveredTotal)

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-canonical-metric"})
	first := readQueuedFrame(t, client)
	if first.Type != model.FrameTypeAckOK {
		t.Fatalf("expected first ack_ok, got %q", first.Type)
	}
	afterFirst := readCounterValue(t, metrics.MessagesDeliveredTotal)
	requireCounterDelta(t, beforeAck, afterFirst, 1, "canonical first ack delivered")

	h.handleAck(client, &model.WSFrame{Type: model.FrameTypeAck, MsgID: "msg-canonical-metric"})
	second := readQueuedFrame(t, client)
	if second.Type != model.FrameTypeAckOK {
		t.Fatalf("expected second ack_ok, got %q", second.Type)
	}
	afterSecond := readCounterValue(t, metrics.MessagesDeliveredTotal)
	requireCounterDelta(t, afterFirst, afterSecond, 0, "canonical second ack delivered")
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
	assertErrorFrameCompat(t, resp, model.ErrorCodeInvalidPayload, "invalid payload_b64")
}

func TestHandleFrameErrorResponsesIncludeCanonicalAndLegacyFields(t *testing.T) {
	tests := []struct {
		name        string
		setupClient func(*model.Client)
		frame       model.WSFrame
		wantCode    model.ErrorCode
		wantMessage string
	}{
		{
			name:        "unknown frame type",
			frame:       model.WSFrame{Type: "mystery"},
			wantCode:    model.ErrorCodeInvalidPayload,
			wantMessage: "unknown frame type",
		},
		{
			name:        "relay frame blocked on client connection",
			frame:       model.WSFrame{Type: model.FrameTypeRelayHello},
			wantCode:    model.ErrorCodeInvalidPayload,
			wantMessage: "relay frame type not allowed on client connections",
		},
		{
			name:        "hello missing wayfarer_id",
			frame:       model.WSFrame{Type: model.FrameTypeHello},
			wantCode:    model.ErrorCodeInvalidWayfarerID,
			wantMessage: "wayfarer_id required",
		},
		{
			name:        "send before hello",
			frame:       model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b", PayloadB64: "QQ=="},
			wantCode:    model.ErrorCodeAuthFailed,
			wantMessage: "not authenticated",
		},
		{
			name: "send after hello missing to",
			setupClient: func(client *model.Client) {
				client.WayfarerID = "wayfarer-a"
			},
			frame:       model.WSFrame{Type: model.FrameTypeSend, PayloadB64: "QQ=="},
			wantCode:    model.ErrorCodeInvalidPayload,
			wantMessage: "recipient required",
		},
		{
			name: "send after hello missing payload_b64",
			setupClient: func(client *model.Client) {
				client.WayfarerID = "wayfarer-a"
			},
			frame:       model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b"},
			wantCode:    model.ErrorCodeInvalidPayload,
			wantMessage: "payload required",
		},
		{
			name: "ack after hello missing msg_id",
			setupClient: func(client *model.Client) {
				client.WayfarerID = "wayfarer-a"
			},
			frame:       model.WSFrame{Type: model.FrameTypeAck},
			wantCode:    model.ErrorCodeInvalidPayload,
			wantMessage: "msg_id required",
		},
		{
			name:        "pull before hello",
			frame:       model.WSFrame{Type: model.FrameTypePull},
			wantCode:    model.ErrorCodeAuthFailed,
			wantMessage: "not authenticated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newWSHandlerWithSpyStore(t)
			client := newClientForWSHandler()
			if tt.setupClient != nil {
				tt.setupClient(client)
			}

			h.handleFrame(client, &tt.frame)

			payload := readQueuedPayload(t, client)
			var resp model.WSFrame
			if err := json.Unmarshal(payload, &resp); err != nil {
				t.Fatalf("decode queued frame: %v", err)
			}
			assertErrorFrameCompat(t, resp, tt.wantCode, tt.wantMessage)

			decoded := decodeFrameMap(t, payload)
			if _, ok := decoded["code"]; !ok {
				t.Fatal("expected canonical code field in error frame")
			}
			if _, ok := decoded["message"]; !ok {
				t.Fatal("expected canonical message field in error frame")
			}
			if _, ok := decoded["msg_id"]; !ok {
				t.Fatal("expected legacy msg_id field in error frame")
			}
		})
	}
}

func TestHandleFrameInternalFailuresMapToInternalError(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*wsStoreSpy)
		frame       model.WSFrame
		wantMessage string
	}{
		{
			name: "persist failure",
			configure: func(st *wsStoreSpy) {
				st.persistErr = errors.New("persist failed")
			},
			frame: model.WSFrame{
				Type:       model.FrameTypeSend,
				To:         "wayfarer-b",
				PayloadB64: "QQ==",
			},
			wantMessage: "failed to persist message",
		},
		{
			name: "ack failure",
			configure: func(st *wsStoreSpy) {
				st.markDeliveredErr = errors.New("ack failed")
			},
			frame: model.WSFrame{
				Type:  model.FrameTypeAck,
				MsgID: "msg-1",
			},
			wantMessage: "failed to acknowledge message",
		},
		{
			name: "pull failure",
			configure: func(st *wsStoreSpy) {
				st.getQueuedErr = errors.New("pull failed")
			},
			frame:       model.WSFrame{Type: model.FrameTypePull},
			wantMessage: "failed to pull messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, st := newWSHandlerWithSpyStore(t)
			if tt.configure != nil {
				tt.configure(st)
			}
			client := newClientForWSHandler()
			client.WayfarerID = "wayfarer-a"

			h.handleFrame(client, &tt.frame)

			resp := readQueuedFrame(t, client)
			assertErrorFrameCompat(t, resp, model.ErrorCodeInternalError, tt.wantMessage)
		})
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
			if sender.GetPayloadEncodingPref() != tt.want {
				t.Fatalf("preference mismatch: got %v want %v", sender.GetPayloadEncodingPref(), tt.want)
			}
		})
	}
}

func TestHandleSendNormalizesPayloadWhitespaceBeforePersist(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	sender := newClientForWSHandler()
	sender.WayfarerID = "wayfarer-a"

	h.handleSend(sender, &model.WSFrame{
		Type:       model.FrameTypeSend,
		To:         "wayfarer-b",
		PayloadB64: " \n\tQQ==\r ",
		TTLSeconds: 120,
	})

	resp := readQueuedFrame(t, sender)
	if resp.Type != model.FrameTypeSendOK {
		t.Fatalf("expected send_ok frame, got %q", resp.Type)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.persisted) != 1 {
		t.Fatalf("expected one persisted message, got %d", len(st.persisted))
	}
	if st.persisted[0].Payload != "QQ==" {
		t.Fatalf("expected normalized payload, got %q", st.persisted[0].Payload)
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
	recipientURL.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64URL)
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
	client.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64URL)

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

func TestHandlePullRemovesCorruptPayloadFromQueue(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	client := newClientForWSHandler()
	client.WayfarerID = "wayfarer-b"
	client.DeliveryID = "wayfarer-b"

	createdAt := time.Unix(1_700_001_100, 0).UTC()
	st.queued[client.WayfarerID] = []*model.Message{{
		ID:        "msg-corrupt-pull",
		From:      "wayfarer-a",
		To:        client.WayfarerID,
		Payload:   "%%%",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	first := readQueuedFrame(t, client)
	if first.Type != model.FrameTypeMessages {
		t.Fatalf("expected messages frame, got %q", first.Type)
	}
	if len(first.Messages) != 0 {
		t.Fatalf("expected no messages after corrupt payload removal, got %d", len(first.Messages))
	}

	h.handlePull(client, &model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	second := readQueuedFrame(t, client)
	if len(second.Messages) != 0 {
		t.Fatalf("expected corrupt message to stay removed, got %d messages", len(second.Messages))
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.removedMsgIDs) != 1 || st.removedMsgIDs[0] != "msg-corrupt-pull" {
		t.Fatalf("unexpected removed message ids: %v", st.removedMsgIDs)
	}
	if queued := st.queued[client.WayfarerID]; len(queued) != 0 {
		t.Fatalf("expected queue to be empty after corruption cleanup, got %d", len(queued))
	}
}

func TestDeliverToRecipientRemovesCorruptPayloadFromQueue(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)

	createdAt := time.Unix(1_700_001_200, 0).UTC()
	msg := &model.Message{
		ID:        "msg-corrupt-deliver",
		From:      "wayfarer-a",
		To:        "wayfarer-b",
		Payload:   "%%%",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Hour),
	}
	st.queued[msg.To] = []*model.Message{msg}

	h.deliverToRecipient(msg)

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.removedMsgIDs) != 1 || st.removedMsgIDs[0] != msg.ID {
		t.Fatalf("unexpected removed message ids: %v", st.removedMsgIDs)
	}
	if queued := st.queued[msg.To]; len(queued) != 0 {
		t.Fatalf("expected queue to be empty after corruption cleanup, got %d", len(queued))
	}
}

func TestDropCorruptMessageSkipsEmptyMsgID(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)

	h.dropCorruptMessage("", "wayfarer-b", "pull", errors.New("decode failed"))

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.removedMsgIDs) != 0 {
		t.Fatalf("expected no remove calls for empty msg_id, got %v", st.removedMsgIDs)
	}
}

func TestDeliverToRecipientDefaultsToLegacyBase64Padding(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	recipient := newClientForWSHandler()
	recipient.WayfarerID = "wayfarer-b"
	recipient.DeliveryID = "wayfarer-b"
	recipient.SetPayloadEncodingPref(model.PayloadEncodingPrefBase64)
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
