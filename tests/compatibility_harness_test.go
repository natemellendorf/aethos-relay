package tests

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/api"
	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

func TestRelayLinkCompatibilityPayloadIntegrityAndAck(t *testing.T) {
	relay, wsURL := startRelayForTest(t, "relay-single", false, "")
	defer relay.close()

	fixture := mustReadFixture(t)
	b64 := base64.StdEncoding.EncodeToString(fixture)

	a := mustDial(t, wsURL)
	defer a.Close()
	b := mustDial(t, wsURL)

	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-a"})
	mustType(t, readFrame(t, a), model.FrameTypeHelloOK)

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-b"})
	mustType(t, readFrame(t, b), model.FrameTypeHelloOK)
	_ = b.Close() // ensure pull path is deterministic (message queued instead of pushed)

	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b", PayloadB64: b64, TTLSeconds: 120})
	sendOK := readFrame(t, a)
	mustType(t, sendOK, model.FrameTypeSendOK)
	if sendOK.MsgID == "" {
		t.Fatal("send_ok must include msg_id")
	}

	stored, err := relay.store.GetMessageByID(context.Background(), sendOK.MsgID)
	if err != nil {
		t.Fatalf("message should exist in store: %v", err)
	}
	if stored.Payload != b64 {
		t.Fatalf("stored payload mutated: got %q want %q", stored.Payload, b64)
	}
	if delta := stored.ExpiresAt.Sub(stored.CreatedAt); delta < 115*time.Second || delta > 125*time.Second {
		t.Fatalf("ttl mismatch: got %v", delta)
	}

	b = mustDial(t, wsURL)
	defer b.Close()
	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-b"})
	mustType(t, readFrame(t, b), model.FrameTypeHelloOK)
	writeFrame(t, b, model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	pulled := readFrame(t, b)
	mustType(t, pulled, model.FrameTypeMessages)
	if len(pulled.Messages) != 1 {
		t.Fatalf("expected exactly one pulled message, got %d", len(pulled.Messages))
	}
	if pulled.Messages[0].ID != sendOK.MsgID {
		t.Fatalf("pulled unexpected msg id: got %s want %s", pulled.Messages[0].ID, sendOK.MsgID)
	}
	gotFixture, err := base64.StdEncoding.DecodeString(pulled.Messages[0].Payload)
	if err != nil {
		t.Fatalf("decode pulled payload: %v", err)
	}
	if string(gotFixture) != string(fixture) {
		t.Fatal("payload bytes changed end-to-end")
	}

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeAck, MsgID: sendOK.MsgID})
	mustType(t, readFrame(t, b), model.FrameTypeAckOK)

	writeFrame(t, b, model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	empty := readFrame(t, b)
	mustType(t, empty, model.FrameTypeMessages)
	if len(empty.Messages) != 0 {
		t.Fatalf("expected empty queue after ack, got %d", len(empty.Messages))
	}
}

func TestRelayLinkCompatibilityFederationOptional(t *testing.T) {
	if os.Getenv("AETHOS_RELAY_TEST_FEDERATION") != "1" {
		t.Skip("set AETHOS_RELAY_TEST_FEDERATION=1 to enable federation integration test")
	}

	relay2, ws2 := startRelayForTest(t, "relay-2", true, "")
	defer relay2.close()
	relay1, ws1 := startRelayForTest(t, "relay-1", true, relay2.fedURL)
	defer relay1.close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if relay1.peerManager.GetPeerCount() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if relay1.peerManager.GetPeerCount() == 0 {
		t.Fatal("relay1 failed to establish federation peer")
	}

	fixture := mustReadFixture(t)
	b64 := base64.StdEncoding.EncodeToString(fixture)

	a := mustDial(t, ws1)
	defer a.Close()
	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-a"})
	mustType(t, readFrame(t, a), model.FrameTypeHelloOK)
	writeFrame(t, a, model.WSFrame{Type: model.FrameTypeSend, To: "wayfarer-b", PayloadB64: b64, TTLSeconds: 180})
	sendOK := readFrame(t, a)
	mustType(t, sendOK, model.FrameTypeSendOK)

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := relay2.store.GetQueuedMessages(context.Background(), "wayfarer-b", 10)
		if err == nil && len(msgs) == 1 {
			if msgs[0].Payload != b64 {
				t.Fatalf("federated payload mutated: got %q want %q", msgs[0].Payload, b64)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	b := mustDial(t, ws2)
	defer b.Close()
	writeFrame(t, b, model.WSFrame{Type: model.FrameTypeHello, WayfarerID: "wayfarer-b"})
	mustType(t, readFrame(t, b), model.FrameTypeHelloOK)
	writeFrame(t, b, model.WSFrame{Type: model.FrameTypePull, Limit: 10})
	pulled := readFrame(t, b)
	mustType(t, pulled, model.FrameTypeMessages)
	if len(pulled.Messages) == 0 {
		t.Fatal("expected federated message on relay2")
	}
	decoded, err := base64.StdEncoding.DecodeString(pulled.Messages[0].Payload)
	if err != nil {
		t.Fatalf("decode federated payload: %v", err)
	}
	if string(decoded) != string(fixture) {
		t.Fatal("federated payload changed end-to-end")
	}
}

type relayHarness struct {
	store       *store.BBoltStore
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

func startRelayForTest(t *testing.T, relayID string, federationEnabled bool, peerURL string) (*relayHarness, string) {
	t.Helper()
	dir := t.TempDir()
	st := store.NewBBoltStore(filepath.Join(dir, "relay.db"))
	if err := st.Open(); err != nil {
		t.Fatalf("open store: %v", err)
	}
	clients := model.NewClientRegistry()
	go clients.Run()
	wsHandler := api.NewWSHandler(st, clients, 24*time.Hour, "", true)
	wsHandler.SetAutoDeliverQueued(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.HandleWebSocket)

	var pm *federation.PeerManager
	if federationEnabled {
		pm = federation.NewPeerManager(relayID, st, clients, 24*time.Hour)
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
	return &relayHarness{store: st, server: srv, peerManager: pm, fedURL: fedURL}, wsURL
}

func mustReadFixture(t *testing.T) []byte {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("fixtures", "envelope_v1_deterministic.cbor.b64"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decode fixture base64: %v", err)
	}
	return raw
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

func mustType(t *testing.T, frame model.WSFrame, want string) {
	t.Helper()
	if frame.Type != want {
		t.Fatalf("unexpected frame type: got %q want %q", frame.Type, want)
	}
}
