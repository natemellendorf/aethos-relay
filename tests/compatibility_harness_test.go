package tests

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/api"
	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

func TestCompatibilityHarnessCanonicalClientRelayPath(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-harness", false, "", true, true)
	defer relay.close()

	a := mustDial(t, wsURL)
	defer a.Close()
	b := mustDial(t, wsURL)
	defer b.Close()

	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeviceID: "device-a"})
	aHelloOK := readFrame(t, a)
	if aHelloOK.RelayID == "" {
		t.Fatal("hello_ok must include relay_id")
	}

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", DeviceID: "device-b"})
	bHelloOK := readFrame(t, b)
	if bHelloOK.RelayID == "" {
		t.Fatal("hello_ok must include relay_id")
	}

	payload := "AQEBAAAAILu7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7AgAAABNtYW5pZmVzdC1maXhlZC0wMDAxAwAAAAxoZWxsby1hZXRob3M"
	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeSend, To: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PayloadB64: payload, TTLSeconds: 120})
	sendOK := readFrame(t, a)
	if sendOK.Type != model.FrameTypeSendOK {
		t.Fatalf("expected send_ok, got %q", sendOK.Type)
	}
	if sendOK.MsgID == "" || sendOK.ReceivedAt == 0 || sendOK.ExpiresAt == 0 {
		t.Fatalf("send_ok missing canonical fields: %+v", sendOK)
	}

	push := readFrame(t, b)
	if push.Type != model.FrameTypeMessage {
		t.Fatalf("expected message push, got %q", push.Type)
	}
	if push.PayloadB64 != payload {
		t.Fatalf("expected canonical payload_b64, got %q", push.PayloadB64)
	}
	if push.ReceivedAt == 0 {
		t.Fatal("message must include received_at")
	}

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	pulledBeforeAck := readFrame(t, b)
	if pulledBeforeAck.Type != model.FrameTypeMessages || len(pulledBeforeAck.Messages) != 1 {
		t.Fatalf("expected 1 pulled message before ack, got %+v", pulledBeforeAck)
	}
	entry := pulledBeforeAck.Messages[0]
	if entry.MsgID == "" || entry.From == "" || entry.PayloadB64 == "" || entry.ReceivedAt == 0 {
		t.Fatalf("pull entry missing canonical fields: %+v", entry)
	}

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeAck, MsgID: sendOK.MsgID})
	ackOK := readFrame(t, b)
	if ackOK.Type != model.FrameTypeAckOK || ackOK.MsgID != sendOK.MsgID {
		t.Fatalf("unexpected ack_ok: %+v", ackOK)
	}

	ackRecipient := storeforward.DeliveryIdentity("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "device-b")
	acked, err := relay.store.IsAckedBy(context.Background(), sendOK.MsgID, ackRecipient)
	if err != nil {
		t.Fatalf("check ack state: %v", err)
	}
	if !acked {
		t.Fatal("expected canonical ack state to be persisted")
	}

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	pulledAfterAck := readFrame(t, b)
	if pulledAfterAck.Type != model.FrameTypeMessages || len(pulledAfterAck.Messages) != 0 {
		t.Fatalf("expected empty pull after ack, got %+v", pulledAfterAck)
	}
}

func TestCompatibilityHarnessRejectsLegacyShapes(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-harness-reject", false, "", true, true)
	defer relay.close()

	conn := mustDial(t, wsURL)
	defer conn.Close()

	writeFrame(t, conn, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	errFrame := readFrame(t, conn)
	if errFrame.Type != model.FrameTypeError || errFrame.Message != "device_id required" {
		t.Fatalf("expected device_id required error, got %+v", errFrame)
	}

	writeFrame(t, conn, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeviceID: "device-a"})
	_ = readFrame(t, conn)

	legacyPayload := base64.StdEncoding.EncodeToString([]byte{0x41}) // "QQ=="
	writeFrame(t, conn, model.WSFrame{Type: model.FrameTypeSend, To: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PayloadB64: legacyPayload})
	errFrame = readFrame(t, conn)
	if errFrame.Type != model.FrameTypeError || errFrame.Message != "invalid payload_b64" {
		t.Fatalf("expected invalid payload_b64 error, got %+v", errFrame)
	}
	if errFrame.MsgID != "" {
		t.Fatalf("error must not include legacy msg_id alias, got %+v", errFrame)
	}
}

type relayHarness struct {
	store       *store.BBoltStore
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
	_ = r.store.Close()
}

func startRelayForTest(t *testing.T, relayID string, federationEnabled bool, peerURL string, ackDrivenSuppression bool, strictClientRelayV1 bool) (*relayHarness, string) {
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
	wsHandler.SetStrictClientRelayV1(strictClientRelayV1)
	wsHandler.SetAutoDeliverQueued(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.HandleWebSocket)

	var pm *federation.PeerManager
	if federationEnabled {
		pm = federation.NewPeerManager(relayID, st, clients, 24*time.Hour)
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
	return &relayHarness{store: st, clients: clients, server: srv, peerManager: pm, fedURL: fedURL}, wsURL
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

func writeFrame(t *testing.T, conn *websocket.Conn, frame model.WSFrame) {
	t.Helper()
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write frame %+v: %v", frame, err)
	}
}

func readFrame(t *testing.T, conn *websocket.Conn) model.WSFrame {
	t.Helper()
	var frame model.WSFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}
