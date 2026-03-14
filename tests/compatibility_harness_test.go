package tests

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/api"
	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

func TestCompatibilityHarnessCanonicalClientRelayPath(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-harness", false, "", true)
	defer relay.close()

	a := mustDial(t, wsURL)
	defer a.Close()
	b := mustDial(t, wsURL)
	defer b.Close()

	aLocalHello := readEnvelope(t, a)
	bLocalHello := readEnvelope(t, b)
	assertFrameType(t, aLocalHello, gossipv1.FrameTypeHello)
	assertFrameType(t, bLocalHello, gossipv1.FrameTypeHello)

	writeEnvelope(t, a, gossipv1.FrameTypeHello, gossipv1.BuildRelayHello("client-a"))
	writeEnvelope(t, b, gossipv1.FrameTypeHello, gossipv1.BuildRelayHello("client-b"))

	aSummary := readEnvelope(t, a)
	bSummary := readEnvelope(t, b)
	assertFrameType(t, aSummary, gossipv1.FrameTypeSummary)
	assertFrameType(t, bSummary, gossipv1.FrameTypeSummary)

	writeEnvelope(t, a, gossipv1.FrameTypeSummary, map[string]any{"have": []string{"msg-1"}})
	aRequest := readEnvelope(t, a)
	assertFrameType(t, aRequest, gossipv1.FrameTypeRequest)
	requestPayload, err := gossipv1.ParseRequestPayload(aRequest.Payload)
	if err != nil {
		t.Fatalf("parse request payload: %v", err)
	}
	if len(requestPayload.Want) != 1 || requestPayload.Want[0] != "msg-1" {
		t.Fatalf("unexpected want set: %#v", requestPayload.Want)
	}

	now := time.Now().UTC()
	writeEnvelope(t, a, gossipv1.FrameTypeTransfer, gossipv1.TransferPayload{Objects: []gossipv1.TransferObject{
		{
			ID:         "msg-1",
			From:       "sender-a",
			To:         "client-a",
			PayloadB64: "QQ",
			CreatedAt:  now.Unix(),
			ExpiresAt:  now.Add(time.Hour).Unix(),
		},
		{
			ID:         "msg-invalid",
			From:       "sender-a",
			To:         "client-a",
			PayloadB64: "invalid",
			CreatedAt:  now.Unix(),
			ExpiresAt:  now.Add(time.Hour).Unix(),
		},
	}})

	aReceipt := readEnvelope(t, a)
	assertFrameType(t, aReceipt, gossipv1.FrameTypeReceipt)
	receiptPayload, err := gossipv1.ParseReceiptPayload(aReceipt.Payload)
	if err != nil {
		t.Fatalf("parse receipt payload: %v", err)
	}
	if len(receiptPayload.Accepted) != 1 || receiptPayload.Accepted[0] != "msg-1" {
		t.Fatalf("unexpected accepted ids: %#v", receiptPayload.Accepted)
	}
	if len(receiptPayload.Rejected) != 1 || receiptPayload.Rejected[0].ID != "msg-invalid" {
		t.Fatalf("unexpected rejected list: %#v", receiptPayload.Rejected)
	}

	if _, err := relay.store.GetMessageByID(context.Background(), "msg-1"); err != nil {
		t.Fatalf("expected accepted message persisted: %v", err)
	}
	if _, err := relay.store.GetMessageByID(context.Background(), "msg-invalid"); err == nil {
		t.Fatal("expected rejected message to remain unpersisted")
	}
}

func TestCompatibilityHarnessRejectsNonBinaryFrames(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-harness-reject", false, "", true)
	defer relay.close()

	conn := mustDial(t, wsURL)
	defer conn.Close()

	_ = readEnvelope(t, conn)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"hello"}`)); err != nil {
		t.Fatalf("write text message: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection close after non-binary message")
	}
}

type relayHarness struct {
	store       *store.BBoltStore
	envelope    *store.BBoltEnvelopeStore
	clients     *model.ClientRegistry
	server      *httptest.Server
	peerManager *federation.PeerManager
	fedURL      string
}

func (r *relayHarness) close() {
	if r.peerManager != nil {
		r.peerManager.Stop()
	}
	r.server.Close()
	if r.envelope != nil {
		_ = r.envelope.Close()
	}
	_ = r.store.Close()
}

func startRelayForTest(t *testing.T, relayID string, federationEnabled bool, peerURL string, ackDrivenSuppression bool) (*relayHarness, string) {
	t.Helper()
	dir := t.TempDir()
	st := store.NewBBoltStore(dir + "/relay.db")
	if err := st.Open(); err != nil {
		t.Fatalf("open store: %v", err)
	}
	clients := model.NewClientRegistry()
	go clients.Run()

	wsHandler := api.NewWSHandler(st, clients, 24*time.Hour, "", true, relayID)
	wsHandler.SetAckDrivenSuppression(ackDrivenSuppression)
	wsHandler.SetAutoDeliverQueued(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.HandleWebSocket)

	var pm *federation.PeerManager
	var envelopeStore *store.BBoltEnvelopeStore
	if federationEnabled {
		envelopeStore = store.NewBBoltEnvelopeStore(dir + "/relay.db.envelopes")
		if err := envelopeStore.Open(); err != nil {
			t.Fatalf("open envelope store: %v", err)
		}

		pm = federation.NewPeerManager(relayID, st, clients, 24*time.Hour)
		pm.SetEnvelopeStore(envelopeStore)
		pm.SetAckDrivenSuppression(ackDrivenSuppression)
		wsHandler.SetFederationManager(pm)
		mux.HandleFunc("/federation/ws", pm.HandleInboundPeer)
		go pm.Run()
	}

	srv := httptest.NewServer(mux)
	if pm != nil && peerURL != "" {
		pm.AddPeerURL(peerURL)
	}

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"
	fedURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/federation/ws"
	return &relayHarness{store: st, envelope: envelopeStore, clients: clients, server: srv, peerManager: pm, fedURL: fedURL}, wsURL
}

func mustDial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	return conn
}

func writeEnvelope(t *testing.T, conn *websocket.Conn, frameType string, payload any) {
	t.Helper()
	frame, err := gossipv1.EncodeEnvelope(frameType, payload)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	packet, err := gossipv1.EncodeLengthPrefixed(frame)
	if err != nil {
		t.Fatalf("encode length prefix: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
}

func readEnvelope(t *testing.T, conn *websocket.Conn) gossipv1.DecodedEnvelope {
	t.Helper()
	msgType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary message, got %d", msgType)
	}
	if len(payload) < 4 {
		t.Fatalf("binary message too short: %d", len(payload))
	}
	frameLen := binary.BigEndian.Uint32(payload[:4])
	if int(frameLen) != len(payload)-4 {
		t.Fatalf("length prefix mismatch: prefix=%d actual=%d", frameLen, len(payload)-4)
	}
	decoded, err := gossipv1.DecodeEnvelope(payload[4:])
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return decoded
}

func assertFrameType(t *testing.T, envelope gossipv1.DecodedEnvelope, want string) {
	t.Helper()
	if envelope.Type != want {
		t.Fatalf("unexpected frame type: got %q want %q", envelope.Type, want)
	}
}
