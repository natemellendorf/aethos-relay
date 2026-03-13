package api

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

type wsStoreSpy struct {
	queuedByRecipient map[string][]string
	messagesByID      map[string]*model.Message
	ackedByMsg        map[string]map[string]bool
	persistedIDs      []string
	persistErrForID   map[string]error
	ackErr            error
}

func newWSStoreSpy() *wsStoreSpy {
	return &wsStoreSpy{
		queuedByRecipient: make(map[string][]string),
		messagesByID:      make(map[string]*model.Message),
		ackedByMsg:        make(map[string]map[string]bool),
		persistErrForID:   make(map[string]error),
	}
}

func (s *wsStoreSpy) Open() error  { return nil }
func (s *wsStoreSpy) Close() error { return nil }

func (s *wsStoreSpy) PersistMessage(ctx context.Context, msg *model.Message) error {
	if msg == nil {
		return errors.New("nil message")
	}
	if err := s.persistErrForID[msg.ID]; err != nil {
		return err
	}
	copy := *msg
	s.messagesByID[msg.ID] = &copy
	s.queuedByRecipient[msg.To] = append(s.queuedByRecipient[msg.To], msg.ID)
	s.persistedIDs = append(s.persistedIDs, msg.ID)
	return nil
}

func (s *wsStoreSpy) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	return s.GetQueuedMessagesRaw(ctx, recipientID, limit)
}

func (s *wsStoreSpy) GetQueuedMessagesRaw(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	ids := s.queuedByRecipient[recipientID]
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*model.Message, 0, len(ids))
	for _, id := range ids {
		if msg, ok := s.messagesByID[id]; ok {
			copy := *msg
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (s *wsStoreSpy) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	return nil
}

func (s *wsStoreSpy) MarkAcked(ctx context.Context, msgID string, recipientID string) (bool, error) {
	if s.ackErr != nil {
		return false, s.ackErr
	}
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
	if s.ackedByMsg[msgID] == nil {
		return false, nil
	}
	return s.ackedByMsg[msgID][recipientID], nil
}

func (s *wsStoreSpy) GetMessageByID(ctx context.Context, msgID string) (*model.Message, error) {
	msg, ok := s.messagesByID[msgID]
	if !ok {
		return nil, errors.New("message not found")
	}
	copy := *msg
	return &copy, nil
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
	ids := s.queuedByRecipient[to]
	idsCopy := make([]string, len(ids))
	copy(idsCopy, ids)
	return idsCopy, nil
}

func newWSHandlerWithSpyStore(t *testing.T) (*WSHandler, *wsStoreSpy) {
	t.Helper()
	st := newWSStoreSpy()
	clients := model.NewClientRegistry()
	go clients.Run()
	h := NewWSHandler(st, clients, 24*time.Hour, "", true, "relay-test")
	h.SetAutoDeliverQueued(false)
	return h, st
}

func decodeEnvelopePayloadMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	if len(payload) < 5 {
		t.Fatalf("payload too short: %d", len(payload))
	}

	frameLen := binary.BigEndian.Uint32(payload[:4])
	if int(frameLen) != len(payload[4:]) {
		t.Fatalf("length prefix mismatch: prefix=%d payload=%d", frameLen, len(payload[4:]))
	}

	decoded, err := gossipv1.DecodeEnvelope(payload[4:])
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return decoded.Payload
}

func TestComputeMissingWantReturnsUnknownIDs(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	st.messagesByID["msg-1"] = &model.Message{ID: "msg-1"}

	want, err := h.computeMissingWant([]string{"msg-1", "msg-2"})
	if err != nil {
		t.Fatalf("computeMissingWant failed: %v", err)
	}
	if len(want) != 1 || want[0] != "msg-2" {
		t.Fatalf("unexpected want set: %#v", want)
	}
}

func TestHandleTransferMixedValidityPersistsOnlyValid(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 4)}}

	now := time.Now().UTC()
	event := gossipv1.Event{
		Type: gossipv1.EventTypeTransfer,
		Transfer: &gossipv1.ParsedTransferPayload{
			Objects: []gossipv1.IndexedTransferObject{
				{Index: 0, Object: gossipv1.TransferObject{
					ID:         "msg-valid",
					From:       "sender-a",
					To:         "recipient-a",
					PayloadB64: "QQ",
					CreatedAt:  now.Add(-time.Minute).Unix(),
					ExpiresAt:  now.Add(time.Hour).Unix(),
				}},
				{Index: 1, Object: gossipv1.TransferObject{
					ID:         "msg-bad",
					From:       "sender-a",
					To:         "recipient-a",
					PayloadB64: "not_base64",
					CreatedAt:  now.Add(-time.Minute).Unix(),
					ExpiresAt:  now.Add(time.Hour).Unix(),
				}},
			},
		},
	}

	if err := h.handleTransfer(session, event); err != nil {
		t.Fatalf("handleTransfer failed: %v", err)
	}

	if len(st.persistedIDs) != 1 || st.persistedIDs[0] != "msg-valid" {
		t.Fatalf("expected only msg-valid persisted, got %#v", st.persistedIDs)
	}

	select {
	case outbound := <-session.client.Send:
		payload := decodeEnvelopePayloadMap(t, outbound)
		accepted, _ := payload["accepted"].([]any)
		if len(accepted) != 1 || accepted[0] != "msg-valid" {
			t.Fatalf("unexpected accepted list: %#v", payload["accepted"])
		}
		rejected, _ := payload["rejected"].([]any)
		if len(rejected) != 1 {
			t.Fatalf("expected one rejected object, got %#v", payload["rejected"])
		}
	default:
		t.Fatal("expected receipt frame")
	}
}

func TestHandleReceiptAcksAcceptedOnly(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{DeliveryID: "wayfarer\x00device"}}

	receipt := gossipv1.ReceiptPayload{
		Accepted: []string{"msg-a", "msg-b"},
		Rejected: []gossipv1.TransferObjectRejection{{Index: 2, ID: "msg-c", Reason: "invalid"}},
	}

	if err := h.handleReceipt(session, gossipv1.Event{Type: gossipv1.EventTypeReceipt, Receipt: &receipt}); err != nil {
		t.Fatalf("handleReceipt failed: %v", err)
	}

	ackedA, _ := st.IsAckedBy(context.Background(), "msg-a", "wayfarer\x00device")
	ackedB, _ := st.IsAckedBy(context.Background(), "msg-b", "wayfarer\x00device")
	ackedC, _ := st.IsAckedBy(context.Background(), "msg-c", "wayfarer\x00device")

	if !ackedA || !ackedB || ackedC {
		t.Fatalf("unexpected ack states: a=%t b=%t c=%t", ackedA, ackedB, ackedC)
	}
}

func TestHandleRequestEmptyWantIsNoOp(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 1), DeliveryID: "client\x00device"}}

	err := h.handleRequest(session, gossipv1.Event{Type: gossipv1.EventTypeRequest, Request: &gossipv1.RequestPayload{Want: []string{}}})
	if err != nil {
		t.Fatalf("handleRequest failed: %v", err)
	}

	select {
	case outbound := <-session.client.Send:
		t.Fatalf("expected no outbound transfer, got %d bytes", len(outbound))
	default:
	}
}

func TestHandleTransferDuplicateIsIdempotent(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 1), DeliveryID: "client\x00device"}}

	now := time.Now().UTC()
	st.messagesByID["msg-dup"] = &model.Message{
		ID:        "msg-dup",
		From:      "sender-a",
		To:        "recipient-a",
		Payload:   "QQ",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
	}

	event := gossipv1.Event{
		Type: gossipv1.EventTypeTransfer,
		Transfer: &gossipv1.ParsedTransferPayload{Objects: []gossipv1.IndexedTransferObject{{
			Index: 0,
			Object: gossipv1.TransferObject{
				ID:         "msg-dup",
				From:       "sender-a",
				To:         "recipient-a",
				PayloadB64: "QQ",
				CreatedAt:  now.Add(-time.Minute).Unix(),
				ExpiresAt:  now.Add(time.Hour).Unix(),
			},
		}}},
	}

	if err := h.handleTransfer(session, event); err != nil {
		t.Fatalf("handleTransfer failed: %v", err)
	}
	if len(st.persistedIDs) != 0 {
		t.Fatalf("duplicate transfer should not persist again, got %#v", st.persistedIDs)
	}

	select {
	case outbound := <-session.client.Send:
		payload := decodeEnvelopePayloadMap(t, outbound)
		accepted, _ := payload["accepted"].([]any)
		if len(accepted) != 1 || accepted[0] != "msg-dup" {
			t.Fatalf("unexpected receipt accepted list: %#v", payload["accepted"])
		}
	default:
		t.Fatal("expected receipt frame")
	}
}
