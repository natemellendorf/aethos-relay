package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
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

func newEnvelopeStoreForTest(t *testing.T) *store.BBoltEnvelopeStore {
	t.Helper()

	envelopeStore := store.NewBBoltEnvelopeStore(filepath.Join(t.TempDir(), "relay.db.envelopes"))
	if err := envelopeStore.Open(); err != nil {
		t.Fatalf("open envelope store: %v", err)
	}
	t.Cleanup(func() {
		_ = envelopeStore.Close()
	})

	return envelopeStore
}

func decodeEnvelopePayloadMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	decoded := decodeEnvelope(t, payload)
	return decoded.Payload
}

func decodeEnvelope(t *testing.T, payload []byte) gossipv1.DecodedEnvelope {
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
	return decoded
}

func TestComputeMissingWantReturnsUnknownIDs(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	known := strings.Repeat("01", 32)
	unknown := strings.Repeat("02", 32)
	st.messagesByID[known] = &model.Message{ID: known}
	summary := &gossipv1.SummaryPayload{BloomFilter: gossipv1.BuildSummaryPayload([]string{known, unknown}).BloomFilter, PreviewItemIDs: []string{known, unknown}, PreviewCursor: unknown}

	want, err := h.computeMissingWant(summary, nil, gossipv1.MaxWantItems)
	if err != nil {
		t.Fatalf("computeMissingWant failed: %v", err)
	}
	if len(want) != 1 || want[0] != unknown {
		t.Fatalf("unexpected want set: %#v", want)
	}
}

func TestComputeMissingWantAppliesPreviewPriorityCapAndFinalSort(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	candidateOnly := strings.Repeat("01", 32)
	previewOnly := strings.Repeat("ff", 32)
	bloomIDs := []string{candidateOnly, previewOnly}
	summary := gossipv1.BuildSummaryPreviewPayload(bloomIDs, []string{previewOnly}, "")

	// peer cap should keep preview-priority item first.
	want, err := h.computeMissingWant(&summary, []string{candidateOnly}, 1)
	if err != nil {
		t.Fatalf("computeMissingWant cap=1 failed: %v", err)
	}
	if len(want) != 1 || want[0] != previewOnly {
		t.Fatalf("unexpected preview-priority want set: %#v", want)
	}

	// with remaining capacity, candidates are added and final output must be digest-sorted.
	want, err = h.computeMissingWant(&summary, []string{candidateOnly}, 2)
	if err != nil {
		t.Fatalf("computeMissingWant cap=2 failed: %v", err)
	}
	if len(want) != 2 || want[0] != candidateOnly || want[1] != previewOnly {
		t.Fatalf("unexpected final sorted want set: %#v", want)
	}
}

func TestHandleTransferMixedValidityPersistsOnlyValid(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 4)}}

	now := time.Now().UTC()
	validCreatedAt := now.Add(-time.Minute).Unix()
	validExpiresAt := now.Add(time.Hour).Unix()
	validObject := mustTransferObject(t, strings.Repeat("bb", 32), "recipient-a", "QQ", validCreatedAt, validExpiresAt)
	invalidObject := validObject
	invalidObject.EnvelopeB64 = "%%%"
	invalidID := strings.Repeat("ab", 32)
	validID := validObject.ItemID
	event := gossipv1.Event{
		Type: gossipv1.EventTypeTransfer,
		Transfer: &gossipv1.ParsedTransferPayload{
			Objects: []gossipv1.IndexedTransferObject{
				{Index: 0, Object: validObject},
				{Index: 1, Object: invalidObject},
			},
			Rejected: []gossipv1.TransferObjectRejection{{Index: 1, ID: invalidID, Reason: "invalid_envelope_encoding", Detail: "invalid base64url"}},
		},
	}

	if err := h.handleTransfer(session, event); err != nil {
		t.Fatalf("handleTransfer failed: %v", err)
	}

	if len(st.persistedIDs) != 1 || st.persistedIDs[0] != validID {
		t.Fatalf("expected only msg-valid persisted, got %#v", st.persistedIDs)
	}

	select {
	case outbound := <-session.client.Send:
		payload := decodeEnvelopePayloadMap(t, outbound)
		received, _ := payload["received"].([]any)
		if len(received) != 1 || received[0] != validID {
			t.Fatalf("unexpected received list: %#v", payload["received"])
		}
		if _, hasAccepted := payload["accepted"]; hasAccepted {
			t.Fatalf("receipt payload must not include accepted key: %#v", payload)
		}
		if _, hasRejected := payload["rejected"]; hasRejected {
			t.Fatalf("receipt payload must not include rejected key: %#v", payload)
		}
	default:
		t.Fatal("expected receipt frame")
	}
}

func TestHandleTransferRejectsItemIDHashMismatch(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 2)}}

	now := time.Now().UTC()
	createdAt := now.Add(-time.Minute).Unix()
	expiresAt := now.Add(time.Hour).Unix()
	validObject := mustTransferObject(t, strings.Repeat("bb", 32), "recipient-a", "QQ", createdAt, expiresAt)
	validID := validObject.ItemID
	mismatchedID := strings.Repeat("ab", 32)
	if mismatchedID == validID {
		mismatchedID = strings.Repeat("ac", 32)
	}

	event := gossipv1.Event{
		Type: gossipv1.EventTypeTransfer,
		Transfer: &gossipv1.ParsedTransferPayload{
			Objects: []gossipv1.IndexedTransferObject{{
				Index: 0,
				Object: func() gossipv1.TransferObject {
					obj := validObject
					obj.ItemID = mismatchedID
					return obj
				}(),
			}},
		},
	}

	if err := h.handleTransfer(session, event); err != nil {
		t.Fatalf("handleTransfer failed: %v", err)
	}
	if len(st.persistedIDs) != 0 {
		t.Fatalf("mismatched id transfer must not persist, got %#v", st.persistedIDs)
	}

	select {
	case outbound := <-session.client.Send:
		decoded := decodeEnvelope(t, outbound)
		receipt, err := gossipv1.ParseReceiptPayload(decoded.Payload)
		if err != nil {
			t.Fatalf("parse receipt payload: %v", err)
		}
		if len(receipt.Accepted) != 0 {
			t.Fatalf("expected no accepted ids, got %#v", receipt.Accepted)
		}
		if _, ok := decoded.Payload["rejected"]; ok {
			t.Fatalf("receipt payload must not include rejected key: %#v", decoded.Payload)
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
	createdAt := now.Add(-time.Minute)
	expiresAt := now.Add(time.Hour)
	duplicateObject := mustTransferObject(t, strings.Repeat("bb", 32), "recipient-a", "QQ", createdAt.Unix(), expiresAt.Unix())
	duplicateID := duplicateObject.ItemID
	st.messagesByID[duplicateID] = &model.Message{
		ID:        duplicateID,
		From:      strings.Repeat("cc", 32),
		To:        "recipient-a",
		Payload:   "QQ",
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}

	event := gossipv1.Event{
		Type: gossipv1.EventTypeTransfer,
		Transfer: &gossipv1.ParsedTransferPayload{Objects: []gossipv1.IndexedTransferObject{{
			Index:  0,
			Object: duplicateObject,
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
		received, _ := payload["received"].([]any)
		if len(received) != 1 || received[0] != duplicateID {
			t.Fatalf("unexpected receipt received list: %#v", payload["received"])
		}
		if _, hasAccepted := payload["accepted"]; hasAccepted {
			t.Fatalf("receipt payload must not include accepted key: %#v", payload)
		}
	default:
		t.Fatal("expected receipt frame")
	}
}

func TestHandleHelloValidatedEmitsCanonicalSummaryOnly(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	clientHello := gossipv1.BuildRelayHello("client-summary-canonical")
	queueRecipient := storeforward.QueueRecipient(storeforward.DeliveryIdentity(clientHello.NodeID, clientHello.NodePubKey))
	now := time.Now().UTC()
	digestA := gossipv1.ComputeItemID("sender-a", queueRecipient, "QQ", now.Unix(), now.Add(time.Hour).Unix())
	digestB := gossipv1.ComputeItemID("sender-b", queueRecipient, "Qg", now.Add(time.Second).Unix(), now.Add(time.Hour).Unix())
	st.queuedByRecipient[queueRecipient] = []string{digestB, "", digestA, digestA}
	st.messagesByID[digestA] = &model.Message{ID: digestA, To: queueRecipient, ExpiresAt: time.Now().Add(time.Hour)}
	st.messagesByID[digestB] = &model.Message{ID: digestB, To: queueRecipient, ExpiresAt: time.Now().Add(time.Hour)}
	envelopeStore := newEnvelopeStoreForTest(t)
	h.SetEnvelopeStore(envelopeStore)
	for _, id := range []string{digestA, digestB} {
		if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{ID: id, DestinationID: queueRecipient, OpaquePayload: []byte("QQ"), OriginRelayID: "relay", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("persist envelope: %v", err)
		}
	}

	session := &clientSession{
		client:  &model.Client{Send: make(chan []byte, 2)},
		adapter: gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-test"), false),
	}

	err := h.handleHelloValidated(session, gossipv1.Event{Type: gossipv1.EventTypeHelloValidated, Hello: &clientHello})
	if err != nil {
		t.Fatalf("handleHelloValidated failed: %v", err)
	}

	select {
	case outbound := <-session.client.Send:
		decoded := decodeEnvelope(t, outbound)
		if decoded.Type != gossipv1.FrameTypeSummary {
			t.Fatalf("unexpected outbound frame type: %s", decoded.Type)
		}
		if len(decoded.Payload) != 4 {
			t.Fatalf("summary payload must include only canonical keys, got %#v", decoded.Payload)
		}
		if _, hasLegacyHave := decoded.Payload["have"]; hasLegacyHave {
			t.Fatalf("ws summary must not include legacy have key: %#v", decoded.Payload)
		}

		summary, parseErr := gossipv1.ParseSummaryPayload(decoded.Payload)
		if parseErr != nil {
			t.Fatalf("summary payload must be parseable by client decoder: %v", parseErr)
		}
		if summary.ItemCount != uint64(len(summary.PreviewItemIDs)) {
			t.Fatalf("unexpected summary item_count: got=%d want=%d", summary.ItemCount, len(summary.PreviewItemIDs))
		}
		if len(summary.PreviewItemIDs) < 2 {
			t.Fatalf("unexpected summary preview_item_ids: %#v", summary.PreviewItemIDs)
		}
		contains := func(ids []string, target string) bool {
			for _, id := range ids {
				if id == target {
					return true
				}
			}
			return false
		}
		if !contains(summary.PreviewItemIDs, digestA) || !contains(summary.PreviewItemIDs, digestB) {
			t.Fatalf("summary preview must include canonical queued IDs: %#v", summary.PreviewItemIDs)
		}
		if !gossipv1.BloomFilterMightContain(summary.BloomFilter, digestA) {
			t.Fatal("summary bloom filter must include digestA")
		}
		if !gossipv1.BloomFilterMightContain(summary.BloomFilter, digestB) {
			t.Fatal("summary bloom filter must include digestB")
		}
	default:
		t.Fatal("expected outbound SUMMARY frame")
	}
}

func TestHandleHelloValidatedSummaryUsesCanonicalEnvelopePreview(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	envelopeStore := newEnvelopeStoreForTest(t)
	h.SetEnvelopeStore(envelopeStore)

	clientHello := gossipv1.BuildRelayHello("client-summary-envelope")
	queueRecipient := storeforward.QueueRecipient(storeforward.DeliveryIdentity(clientHello.NodeID, clientHello.NodePubKey))

	// Legacy queue-shaped IDs should not drive preview when envelope store is configured.
	legacyQueueID := "legacy-queue-id"
	st.queuedByRecipient[queueRecipient] = []string{legacyQueueID}
	st.messagesByID[legacyQueueID] = &model.Message{ID: legacyQueueID, To: queueRecipient, ExpiresAt: time.Now().Add(time.Hour)}
	now := time.Now().UTC()
	eligibleID := gossipv1.ComputeItemID("sender-eligible", queueRecipient, "QQ", now.Unix(), now.Add(time.Hour).Unix())
	otherID := gossipv1.ComputeItemID("sender-other", "different-destination", "Qg", now.Add(time.Second).Unix(), now.Add(time.Hour).Unix())

	if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:            eligibleID,
		DestinationID: queueRecipient,
		OpaquePayload: []byte("QQ"),
		OriginRelayID: "relay-test",
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("persist eligible envelope: %v", err)
	}
	if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:            otherID,
		DestinationID: "different-destination",
		OpaquePayload: []byte("QQ"),
		OriginRelayID: "relay-test",
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("persist unrelated envelope: %v", err)
	}

	session := &clientSession{
		client:  &model.Client{Send: make(chan []byte, 2)},
		adapter: gossipv1.NewSessionAdapter(gossipv1.BuildRelayHello("relay-test"), false),
	}

	if err := h.handleHelloValidated(session, gossipv1.Event{Type: gossipv1.EventTypeHelloValidated, Hello: &clientHello}); err != nil {
		t.Fatalf("handleHelloValidated failed: %v", err)
	}

	select {
	case outbound := <-session.client.Send:
		summary, err := gossipv1.ParseSummaryPayload(decodeEnvelope(t, outbound).Payload)
		if err != nil {
			t.Fatalf("parse summary payload: %v", err)
		}
		if len(summary.PreviewItemIDs) != 1 || summary.PreviewItemIDs[0] != eligibleID {
			t.Fatalf("unexpected recipient-aware preview IDs: %#v", summary.PreviewItemIDs)
		}
		for _, id := range summary.PreviewItemIDs {
			if id == legacyQueueID {
				t.Fatalf("summary preview must not use queue-shaped legacy ids: %#v", summary.PreviewItemIDs)
			}
		}
	default:
		t.Fatal("expected outbound SUMMARY frame")
	}
}

func TestHandleSummaryRequestsUnknownPreviewIDs(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 1)}}
	session.peerMaxWant = 2

	knownID := strings.Repeat("0e", 32)
	unknownA := strings.Repeat("0f", 32)
	unknownB := strings.Repeat("10", 32)
	st.messagesByID[knownID] = &model.Message{ID: knownID, ExpiresAt: time.Now().Add(time.Hour)}
	bloom := gossipv1.BuildSummaryPayload([]string{knownID, unknownA, unknownB}).BloomFilter
	event := gossipv1.Event{Type: gossipv1.EventTypeSummary, Summary: &gossipv1.SummaryPayload{BloomFilter: bloom, PreviewItemIDs: []string{knownID, unknownA, unknownB}, PreviewCursor: unknownB}}

	if err := h.handleSummary(session, event); err != nil {
		t.Fatalf("handleSummary failed: %v", err)
	}

	select {
	case outbound := <-session.client.Send:
		decoded := decodeEnvelope(t, outbound)
		if decoded.Type != gossipv1.FrameTypeRequest {
			t.Fatalf("unexpected outbound frame type: %s", decoded.Type)
		}
		request, err := gossipv1.ParseRequestPayload(decoded.Payload)
		if err != nil {
			t.Fatalf("parse request payload: %v", err)
		}
		if len(request.Want) != 2 || request.Want[0] != unknownA || request.Want[1] != unknownB {
			t.Fatalf("unexpected request want set: %#v", request.Want)
		}
	default:
		t.Fatal("expected outbound REQUEST frame")
	}
}

func TestComputeMissingWantUsesEnvelopeStoreAndMessageStoreForKnownObjects(t *testing.T) {
	h, st := newWSHandlerWithSpyStore(t)
	envelopeStore := newEnvelopeStoreForTest(t)
	h.SetEnvelopeStore(envelopeStore)

	objectID := strings.Repeat("11", 32)
	bloom := gossipv1.BuildSummaryPayload([]string{objectID}).BloomFilter
	st.messagesByID[objectID] = &model.Message{ID: objectID, ExpiresAt: time.Now().Add(time.Hour)}
	summary := &gossipv1.SummaryPayload{BloomFilter: bloom, PreviewItemIDs: []string{objectID}, PreviewCursor: objectID}

	want, err := h.computeMissingWant(summary, nil, gossipv1.MaxWantItems)
	if err != nil {
		t.Fatalf("computeMissingWant failed: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("expected known object in message store to suppress request, got %#v", want)
	}

	now := time.Now().UTC()
	if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:            objectID,
		DestinationID: "dest-1",
		OpaquePayload: []byte("QQ"),
		OriginRelayID: "relay-test",
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("persist envelope: %v", err)
	}

	want, err = h.computeMissingWant(summary, nil, gossipv1.MaxWantItems)
	if err != nil {
		t.Fatalf("computeMissingWant after persist failed: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("expected object to remain known after envelope persist, got %#v", want)
	}
}

func TestHandleSummaryRejectsInvalidPreviewCursor(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	session := &clientSession{client: &model.Client{Send: make(chan []byte, 1)}}
	idA := strings.Repeat("12", 32)
	idB := strings.Repeat("13", 32)
	bloom := gossipv1.BuildSummaryPayload([]string{idA, idB}).BloomFilter
	event := gossipv1.Event{Type: gossipv1.EventTypeSummary, Summary: &gossipv1.SummaryPayload{BloomFilter: bloom, PreviewItemIDs: []string{idA, idB}, PreviewCursor: idA}}

	if err := h.handleSummary(session, event); err == nil {
		t.Fatal("expected summary cursor invariant violation")
	}
}

func TestBuildRecipientSummaryUsesContentAddressedInventoryPaging(t *testing.T) {
	h, _ := newWSHandlerWithSpyStore(t)
	envelopeStore := newEnvelopeStoreForTest(t)
	h.SetEnvelopeStore(envelopeStore)

	queueRecipient := "wayfarer-summary-paging"
	now := time.Now().UTC()
	allItemIDs := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		payload := model.EncodePayloadB64([]byte{byte(i + 1)}, model.PayloadEncodingPrefBase64URL)
		createdAt := now.Add(time.Duration(i) * time.Second)
		expiresAt := now.Add(2 * time.Hour)
		itemID := gossipv1.ComputeItemID("sender", queueRecipient, payload, createdAt.Unix(), expiresAt.Unix())
		allItemIDs = append(allItemIDs, itemID)

		if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
			ID:            itemID,
			DestinationID: queueRecipient,
			OpaquePayload: []byte(payload),
			OriginRelayID: "relay-test",
			CreatedAt:     createdAt,
			ExpiresAt:     expiresAt,
		}); err != nil {
			t.Fatalf("persist envelope %d: %v", i, err)
		}
	}

	// Non-content-addressed IDs must never appear in preview inventory.
	if err := envelopeStore.PersistEnvelope(context.Background(), &model.Envelope{
		ID:            "123e4567-e89b-12d3-a456-426614174000",
		DestinationID: queueRecipient,
		OpaquePayload: []byte("legacy"),
		OriginRelayID: "relay-test",
		CreatedAt:     now,
		ExpiresAt:     now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("persist non-digest envelope: %v", err)
	}

	summary, nextCursor, err := h.buildRecipientSummary(queueRecipient, "")
	if err != nil {
		t.Fatalf("build first summary: %v", err)
	}
	if len(summary.BloomFilter) != gossipv1.BloomFilterBytes {
		t.Fatalf("unexpected bloom length: got=%d want=%d", len(summary.BloomFilter), gossipv1.BloomFilterBytes)
	}
	if summary.ItemCount != uint64(len(allItemIDs)) {
		t.Fatalf("unexpected item_count: got=%d want=%d", summary.ItemCount, len(allItemIDs))
	}
	if len(summary.PreviewItemIDs) != int(gossipv1.MaxSummaryPreviewItems) {
		t.Fatalf("unexpected preview size: got=%d want=%d", len(summary.PreviewItemIDs), gossipv1.MaxSummaryPreviewItems)
	}
	if summary.PreviewCursor == "" {
		t.Fatal("expected non-empty preview cursor")
	}
	if summary.PreviewCursor != summary.PreviewItemIDs[len(summary.PreviewItemIDs)-1] {
		t.Fatalf("preview cursor invariant violated: cursor=%q last=%q", summary.PreviewCursor, summary.PreviewItemIDs[len(summary.PreviewItemIDs)-1])
	}
	if nextCursor != summary.PreviewCursor {
		t.Fatalf("unexpected next cursor: got=%q want=%q", nextCursor, summary.PreviewCursor)
	}

	for idx := 1; idx < len(summary.PreviewItemIDs); idx++ {
		if gossipv1.CompareDigestHexIDs(summary.PreviewItemIDs[idx-1], summary.PreviewItemIDs[idx]) >= 0 {
			t.Fatalf("preview ids not strictly sorted at %d: %q >= %q", idx, summary.PreviewItemIDs[idx-1], summary.PreviewItemIDs[idx])
		}
	}

	nextSummary, secondCursor, err := h.buildRecipientSummary(queueRecipient, nextCursor)
	if err != nil {
		t.Fatalf("build second summary: %v", err)
	}
	if len(nextSummary.PreviewItemIDs) == 0 {
		t.Fatal("expected non-empty second preview page")
	}
	if len(nextSummary.PreviewItemIDs) != len(allItemIDs)-int(gossipv1.MaxSummaryPreviewItems) {
		t.Fatalf("unexpected second page size: got=%d", len(nextSummary.PreviewItemIDs))
	}
	for _, id := range nextSummary.PreviewItemIDs {
		if gossipv1.CompareDigestHexIDs(id, nextCursor) <= 0 {
			t.Fatalf("second page id not strictly after cursor: id=%q cursor=%q", id, nextCursor)
		}
	}
	if secondCursor != nextSummary.PreviewCursor {
		t.Fatalf("unexpected second next cursor: got=%q want=%q", secondCursor, nextSummary.PreviewCursor)
	}
}

func mustTransferObject(t *testing.T, manifestID string, to string, payloadB64 string, createdAt int64, expiresAt int64) gossipv1.TransferObject {
	t.Helper()
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		t.Fatalf("canonical cbor mode: %v", err)
	}
	toBytes, err := hex.DecodeString(manifestDestinationIDHex(t, to))
	if err != nil {
		t.Fatalf("decode destination: %v", err)
	}
	manifestBytes, err := hex.DecodeString(manifestID)
	if err != nil {
		t.Fatalf("decode manifest id: %v", err)
	}
	body, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	seed := bytes.Repeat([]byte{0x11}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signingPayload, err := mode.Marshal(map[string]any{"to_wayfarer_id": toBytes, "manifest_id": manifestBytes, "body": body})
	if err != nil {
		t.Fatalf("encode signing payload: %v", err)
	}
	digest := sha256.Sum256(append([]byte("AETHOS_ENVELOPE_V1"), signingPayload...))
	authorSig := ed25519.Sign(privateKey, digest[:])
	envelopeB64, err := gossipv1.EncodeTransferEnvelopeB64(hex.EncodeToString(toBytes), manifestID, payloadB64, publicKey, authorSig)
	if err != nil {
		t.Fatalf("encode transfer envelope: %v", err)
	}
	itemID := gossipv1.ComputeTransferObjectItemID(gossipv1.TransferObject{EnvelopeB64: envelopeB64})
	return gossipv1.TransferObject{
		ItemID:       itemID,
		EnvelopeB64:  envelopeB64,
		ExpiryUnixMS: uint64(expiresAt) * 1000,
		HopCount:     0,
	}
}

func manifestDestinationIDHex(t *testing.T, to string) string {
	t.Helper()
	if gossipv1.IsDigestHexID(to) {
		return to
	}
	digest := sha256.Sum256([]byte(to))
	return hex.EncodeToString(digest[:])
}
