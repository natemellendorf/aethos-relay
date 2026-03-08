package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/storeforward"
)

const conformanceFixtureRoot = "testdata/aethos/client_relay_v1"

const (
	fixtureExpectationCanonicalV1        = "canonical_v1"
	fixtureExpectationTransitionalCompat = "transitional_compat"
)

type protocolFixtureCase struct {
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	ExpectationMode  string                  `json:"expectation_mode"`
	KnownDivergences []string                `json:"known_divergences,omitempty"`
	Relay            protocolFixtureRelay    `json:"relay"`
	Clients          []protocolFixtureClient `json:"clients"`
	Steps            []protocolFixtureStep   `json:"steps"`
}

type protocolFixtureRelay struct {
	AckDrivenSuppression bool `json:"ack_driven_suppression"`
}

type protocolFixtureClient struct {
	ID         string `json:"id"`
	WayfarerID string `json:"wayfarer_id"`
	DeviceID   string `json:"device_id,omitempty"`
}

type protocolFixtureStep struct {
	Op                   string            `json:"op"`
	Client               string            `json:"client,omitempty"`
	FrameRef             string            `json:"frame_ref,omitempty"`
	ToClient             string            `json:"to_client,omitempty"`
	PayloadRef           string            `json:"payload_ref,omitempty"`
	TTLSeconds           int               `json:"ttl_seconds,omitempty"`
	Limit                int               `json:"limit,omitempty"`
	MsgIDVar             string            `json:"msg_id_var,omitempty"`
	CapturePayloadVar    string            `json:"capture_payload_var,omitempty"`
	Capture              map[string]string `json:"capture,omitempty"`
	RequiredFields       []string          `json:"required_fields,omitempty"`
	ForbiddenFields      []string          `json:"forbidden_fields,omitempty"`
	FieldEquals          map[string]string `json:"field_equals,omitempty"`
	EqualFields          [][2]string       `json:"equal_fields,omitempty"`
	Count                int               `json:"count,omitempty"`
	MessageIndex         int               `json:"message_index,omitempty"`
	MessageRequired      []string          `json:"message_required_fields,omitempty"`
	MessageForbidden     []string          `json:"message_forbidden_fields,omitempty"`
	MessageFieldEquals   map[string]string `json:"message_field_equals,omitempty"`
	MessageEqualFields   [][2]string       `json:"message_equal_fields,omitempty"`
	MessagePayloadEquals string            `json:"message_payload_equals,omitempty"`
	Recipient            string            `json:"recipient,omitempty"`
	Delivered            *bool             `json:"delivered,omitempty"`
}

type protocolFixtureRunner struct {
	t        *testing.T
	relay    *relayHarness
	wsURL    string
	clients  map[string]protocolFixtureClient
	conns    map[string]*websocket.Conn
	captures map[string]any
}

func TestProtocolConformanceFixtures(t *testing.T) {
	casePaths := mustListProtocolFixtureCases(t)
	for _, casePath := range casePaths {
		fixtureCase := mustLoadProtocolFixtureCase(t, casePath)
		t.Run(fixtureCase.ExpectationMode+"/"+fixtureCase.Name, func(t *testing.T) {
			runner := newProtocolFixtureRunner(t, fixtureCase)
			defer runner.closeAll()
			runner.runSteps(fixtureCase.Steps)
		})
	}
}

func newProtocolFixtureRunner(t *testing.T, fixtureCase protocolFixtureCase) *protocolFixtureRunner {
	t.Helper()
	relayID := "fixture-" + sanitizeRelayIDSuffix(fixtureCase.Name)
	relay, wsURL := startRelayForTest(t, relayID, false, "", fixtureCase.Relay.AckDrivenSuppression)

	clients := make(map[string]protocolFixtureClient, len(fixtureCase.Clients))
	for _, client := range fixtureCase.Clients {
		clients[client.ID] = client
	}

	return &protocolFixtureRunner{
		t:        t,
		relay:    relay,
		wsURL:    wsURL,
		clients:  clients,
		conns:    make(map[string]*websocket.Conn),
		captures: make(map[string]any),
	}
}

func (r *protocolFixtureRunner) runSteps(steps []protocolFixtureStep) {
	r.t.Helper()
	for index, step := range steps {
		r.runStep(index, step)
	}
}

func (r *protocolFixtureRunner) runStep(index int, step protocolFixtureStep) {
	r.t.Helper()

	switch step.Op {
	case "dial":
		r.dial(step.Client)
	case "close":
		r.closeConn(step.Client)
	case "hello":
		frame := r.loadWSFrame(step.FrameRef)
		client := r.mustClient(step.Client)
		frame.WayfarerID = client.WayfarerID
		frame.DeviceID = client.DeviceID
		writeFrame(r.t, r.mustConn(step.Client), frame)
	case "send":
		frame := r.loadWSFrame(step.FrameRef)
		if step.ToClient != "" {
			frame.To = r.mustClient(step.ToClient).WayfarerID
		}
		if step.PayloadRef != "" {
			payload := strings.TrimSpace(r.readFixtureText(step.PayloadRef))
			frame.PayloadB64 = payload
			if step.CapturePayloadVar != "" {
				r.captures[step.CapturePayloadVar] = payload
			}
		}
		if step.TTLSeconds > 0 {
			frame.TTLSeconds = step.TTLSeconds
		}
		writeFrame(r.t, r.mustConn(step.Client), frame)
	case "pull":
		frame := r.loadWSFrame(step.FrameRef)
		if step.Limit > 0 {
			frame.Limit = step.Limit
		}
		writeFrame(r.t, r.mustConn(step.Client), frame)
	case "ack":
		frame := r.loadWSFrame(step.FrameRef)
		if step.MsgIDVar != "" {
			frame.MsgID = r.mustCaptureString(step.MsgIDVar)
		}
		writeFrame(r.t, r.mustConn(step.Client), frame)
	case "expect_frame":
		r.expectFrame(step)
	case "expect_messages":
		r.expectMessages(step)
	case "assert_delivery_state":
		r.assertDeliveryState(step)
	default:
		r.t.Fatalf("step %d: unsupported op %q", index+1, step.Op)
	}
}

func (r *protocolFixtureRunner) expectFrame(step protocolFixtureStep) {
	r.t.Helper()
	frame := readFrame(r.t, r.mustConn(step.Client))
	actual := frameToMap(r.t, frame)
	expected := r.loadFrameMap(step.FrameRef)

	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			r.t.Fatalf("frame missing expected field %q", key)
		}
		if normalizeComparable(got) != normalizeComparable(want) {
			r.t.Fatalf("frame field mismatch for %q: got %v want %v", key, got, want)
		}
	}

	r.requireFields(actual, step.RequiredFields)
	r.forbidFields(actual, step.ForbiddenFields)
	r.applyFieldEquals(actual, step.FieldEquals)
	r.applyEqualPairs(actual, step.EqualFields)
	r.captureFields(actual, step.Capture)
}

func (r *protocolFixtureRunner) expectMessages(step protocolFixtureStep) {
	r.t.Helper()
	frame := readFrame(r.t, r.mustConn(step.Client))
	actual := frameToMap(r.t, frame)
	expected := r.loadFrameMap(step.FrameRef)

	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			r.t.Fatalf("messages frame missing expected field %q", key)
		}
		if normalizeComparable(got) != normalizeComparable(want) {
			r.t.Fatalf("messages frame field mismatch for %q: got %v want %v", key, got, want)
		}
	}

	messagesAny, ok := actual["messages"].([]any)
	if !ok {
		if _, exists := actual["messages"]; exists {
			r.t.Fatalf("messages field wrong type: %T", actual["messages"])
		}
		messagesAny = []any{}
	}
	if len(messagesAny) != step.Count {
		r.t.Fatalf("unexpected message count: got %d want %d", len(messagesAny), step.Count)
	}
	if step.Count == 0 {
		return
	}

	if step.MessageIndex < 0 || step.MessageIndex >= len(messagesAny) {
		r.t.Fatalf("message_index out of range: %d", step.MessageIndex)
	}

	message, ok := messagesAny[step.MessageIndex].(map[string]any)
	if !ok {
		r.t.Fatalf("message entry is not an object: %T", messagesAny[step.MessageIndex])
	}

	r.requireFields(message, step.MessageRequired)
	r.forbidFields(message, step.MessageForbidden)
	r.applyFieldEquals(message, step.MessageFieldEquals)
	r.applyEqualPairs(message, step.MessageEqualFields)
	r.assertMessagePayloadEquals(message, step.MessagePayloadEquals)
	r.captureFields(message, step.Capture)
}

func (r *protocolFixtureRunner) forbidFields(frame map[string]any, forbidden []string) {
	r.t.Helper()
	for _, field := range forbidden {
		if _, ok := frame[field]; ok {
			r.t.Fatalf("forbidden field present: %q", field)
		}
	}
}

func (r *protocolFixtureRunner) assertMessagePayloadEquals(message map[string]any, expectedToken string) {
	r.t.Helper()
	if expectedToken == "" {
		return
	}
	actualPayloadAny, ok := message["payload_b64"]
	if !ok {
		r.t.Fatal("message_payload_equals requires payload_b64 field")
	}
	actualPayload, ok := actualPayloadAny.(string)
	if !ok {
		r.t.Fatalf("payload_b64 must be string, got %T", actualPayloadAny)
	}

	expectedPayload := r.resolveToken(expectedToken)

	actualBytes, err := model.DecodePayloadB64(actualPayload)
	if err != nil {
		r.t.Fatalf("decode actual payload_b64: %v", err)
	}
	expectedBytes, err := model.DecodePayloadB64(expectedPayload)
	if err != nil {
		r.t.Fatalf("decode expected payload_b64: %v", err)
	}
	if !bytes.Equal(actualBytes, expectedBytes) {
		r.t.Fatalf("decoded payload mismatch for token %q", expectedToken)
	}
}

func (r *protocolFixtureRunner) assertDeliveryState(step protocolFixtureStep) {
	r.t.Helper()
	if step.Delivered == nil {
		r.t.Fatal("assert_delivery_state requires delivered")
	}
	recipient := r.resolveToken(step.Recipient)
	msgID := r.mustCaptureString(step.MsgIDVar)
	got, err := r.relay.store.IsDeliveredTo(context.Background(), msgID, recipient)
	if err != nil {
		r.t.Fatalf("check delivery state: %v", err)
	}
	if got != *step.Delivered {
		r.t.Fatalf("delivery state mismatch for recipient=%q msg_id=%q: got %t want %t", recipient, msgID, got, *step.Delivered)
	}
}

func (r *protocolFixtureRunner) requireFields(frame map[string]any, required []string) {
	r.t.Helper()
	for _, field := range required {
		value, ok := frame[field]
		if !ok {
			r.t.Fatalf("required field missing: %q", field)
		}
		if isEmptyValue(value) {
			r.t.Fatalf("required field empty: %q", field)
		}
	}
}

func (r *protocolFixtureRunner) applyFieldEquals(frame map[string]any, fieldEquals map[string]string) {
	r.t.Helper()
	for field, expectedToken := range fieldEquals {
		got, ok := frame[field]
		if !ok {
			r.t.Fatalf("field_equals missing field: %q", field)
		}
		want := r.resolveTokenValue(expectedToken)
		if normalizeComparable(got) != normalizeComparable(want) {
			r.t.Fatalf("field_equals mismatch for %q: got %v want %v", field, got, want)
		}
	}
}

func (r *protocolFixtureRunner) applyEqualPairs(frame map[string]any, equalPairs [][2]string) {
	r.t.Helper()
	for _, pair := range equalPairs {
		left := r.lookupValue(frame, pair[0])
		right := r.lookupValue(frame, pair[1])
		if normalizeComparable(left) != normalizeComparable(right) {
			r.t.Fatalf("equal_fields mismatch: %q=%v %q=%v", pair[0], left, pair[1], right)
		}
	}
}

func (r *protocolFixtureRunner) captureFields(frame map[string]any, capture map[string]string) {
	r.t.Helper()
	for variable, field := range capture {
		value, ok := frame[field]
		if !ok {
			r.t.Fatalf("capture missing field %q", field)
		}
		r.captures[variable] = value
	}
}

func (r *protocolFixtureRunner) lookupValue(frame map[string]any, keyOrToken string) any {
	if strings.HasPrefix(keyOrToken, "$") {
		return r.resolveTokenValue(keyOrToken)
	}
	value, ok := frame[keyOrToken]
	if !ok {
		r.t.Fatalf("missing field for equal_fields: %q", keyOrToken)
	}
	return value
}

func (r *protocolFixtureRunner) resolveTokenValue(token string) any {
	if !strings.HasPrefix(token, "$") {
		return token
	}
	resolved := r.resolveToken(token)
	return resolved
}

func (r *protocolFixtureRunner) resolveToken(token string) string {
	r.t.Helper()
	if !strings.HasPrefix(token, "$") {
		return token
	}

	key := strings.TrimPrefix(token, "$")
	if captured, ok := r.captures[key]; ok {
		return fmt.Sprint(captured)
	}

	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 2 {
		client, ok := r.clients[parts[0]]
		if !ok {
			r.t.Fatalf("unknown token client id: %q", parts[0])
		}
		switch parts[1] {
		case "wayfarer_id":
			return client.WayfarerID
		case "device_id":
			return client.DeviceID
		case "delivery_identity":
			return storeforward.DeliveryIdentity(client.WayfarerID, client.DeviceID)
		default:
			r.t.Fatalf("unsupported token field: %q", parts[1])
		}
	}

	r.t.Fatalf("unknown token: %q", token)
	return ""
}

func (r *protocolFixtureRunner) mustCaptureString(variable string) string {
	r.t.Helper()
	value, ok := r.captures[variable]
	if !ok {
		r.t.Fatalf("missing captured value for %q", variable)
	}
	return fmt.Sprint(value)
}

func (r *protocolFixtureRunner) dial(clientID string) {
	r.t.Helper()
	r.mustClient(clientID)
	if _, exists := r.conns[clientID]; exists {
		r.t.Fatalf("client %q already dialed", clientID)
	}
	r.conns[clientID] = mustDial(r.t, r.wsURL)
}

func (r *protocolFixtureRunner) closeConn(clientID string) {
	r.t.Helper()
	conn, ok := r.conns[clientID]
	if !ok {
		r.t.Fatalf("client %q is not dialed", clientID)
	}
	client := r.mustClient(clientID)
	_ = conn.Close()
	delete(r.conns, clientID)
	r.waitOffline(client.WayfarerID)
}

func (r *protocolFixtureRunner) waitOffline(wayfarerID string) {
	r.t.Helper()
	if wayfarerID == "" || r.relay == nil || r.relay.clients == nil {
		return
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !r.relay.clients.IsOnline(wayfarerID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %q to go offline", wayfarerID)
}

func (r *protocolFixtureRunner) closeAll() {
	for clientID, conn := range r.conns {
		_ = conn.Close()
		delete(r.conns, clientID)
	}
	r.relay.close()
}

func (r *protocolFixtureRunner) mustConn(clientID string) *websocket.Conn {
	r.t.Helper()
	conn, ok := r.conns[clientID]
	if !ok {
		r.t.Fatalf("client %q is not dialed", clientID)
	}
	return conn
}

func (r *protocolFixtureRunner) mustClient(clientID string) protocolFixtureClient {
	r.t.Helper()
	client, ok := r.clients[clientID]
	if !ok {
		r.t.Fatalf("unknown client %q", clientID)
	}
	return client
}

func (r *protocolFixtureRunner) loadWSFrame(ref string) model.WSFrame {
	r.t.Helper()
	var frame model.WSFrame
	r.unmarshalFixture(ref, &frame)
	return frame
}

func (r *protocolFixtureRunner) loadFrameMap(ref string) map[string]any {
	r.t.Helper()
	if ref == "" {
		return map[string]any{}
	}
	var frame map[string]any
	r.unmarshalFixture(ref, &frame)
	return frame
}

func (r *protocolFixtureRunner) unmarshalFixture(ref string, target any) {
	r.t.Helper()
	raw := []byte(r.readFixtureText(ref))
	if err := json.Unmarshal(raw, target); err != nil {
		r.t.Fatalf("decode fixture %q: %v", ref, err)
	}
}

func (r *protocolFixtureRunner) readFixtureText(ref string) string {
	r.t.Helper()
	if ref == "" {
		r.t.Fatal("fixture ref is required")
	}
	absPath := filepath.Join(conformanceFixtureRoot, filepath.FromSlash(ref))
	raw, err := os.ReadFile(absPath)
	if err != nil {
		r.t.Fatalf("read fixture %q: %v", absPath, err)
	}
	return string(raw)
}

func mustListProtocolFixtureCases(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(conformanceFixtureRoot, "cases"))
	if err != nil {
		t.Fatalf("read conformance cases directory: %v", err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		paths = append(paths, filepath.Join("cases", entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no protocol conformance case fixtures found")
	}
	return paths
}

func mustLoadProtocolFixtureCase(t *testing.T, relativePath string) protocolFixtureCase {
	t.Helper()
	path := filepath.Join(conformanceFixtureRoot, filepath.FromSlash(relativePath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case fixture %q: %v", path, err)
	}

	var fixtureCase protocolFixtureCase
	if err := json.Unmarshal(raw, &fixtureCase); err != nil {
		t.Fatalf("decode case fixture %q: %v", path, err)
	}
	if fixtureCase.Name == "" {
		t.Fatalf("fixture case %q missing name", path)
	}
	switch fixtureCase.ExpectationMode {
	case fixtureExpectationCanonicalV1:
		if len(fixtureCase.KnownDivergences) != 0 {
			t.Fatalf("fixture case %q canonical_v1 must not declare known_divergences", path)
		}
	case fixtureExpectationTransitionalCompat:
		if len(fixtureCase.KnownDivergences) == 0 {
			t.Fatalf("fixture case %q transitional_compat must declare known_divergences", path)
		}
	default:
		t.Fatalf("fixture case %q has unsupported expectation_mode %q", path, fixtureCase.ExpectationMode)
	}
	if len(fixtureCase.Steps) == 0 {
		t.Fatalf("fixture case %q has no steps", path)
	}
	if len(fixtureCase.Clients) == 0 {
		t.Fatalf("fixture case %q has no clients", path)
	}
	return fixtureCase
}

func frameToMap(t *testing.T, frame model.WSFrame) map[string]any {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	var mapped map[string]any
	if err := json.Unmarshal(raw, &mapped); err != nil {
		t.Fatalf("unmarshal frame map: %v", err)
	}
	return mapped
}

func sanitizeRelayIDSuffix(name string) string {
	replacer := strings.NewReplacer(" ", "-", "/", "-", "_", "-", ".", "-")
	sanitized := strings.ToLower(replacer.Replace(name))
	if sanitized == "" {
		return "case"
	}
	return sanitized
}

func normalizeComparable(value any) string {
	return fmt.Sprint(value)
}

func isEmptyValue(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case nil:
		return true
	default:
		return false
	}
}
