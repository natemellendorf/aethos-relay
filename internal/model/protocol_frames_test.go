package model

import (
	"encoding/json"
	"testing"
)

func assertRoundTripType(t *testing.T, frame interface{}, wantType string) {
	t.Helper()

	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}

	var roundTrip struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("decode encoded frame: %v", err)
	}
	if roundTrip.Type != wantType {
		t.Fatalf("round-trip type mismatch: got %q want %q", roundTrip.Type, wantType)
	}
}

func TestClientFrameDecodeEncode(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantType       string
		wantWayfarerID string
		wantDeviceID   string
		wantTo         string
		wantMsgID      string
	}{
		{
			name:           "hello",
			raw:            `{"type":"hello","wayfarer_id":"wayfarer-a"}`,
			wantType:       FrameTypeHello,
			wantWayfarerID: "wayfarer-a",
		},
		{
			name:           "hello with device",
			raw:            `{"type":"hello","wayfarer_id":"wayfarer-a","device_id":"device-1"}`,
			wantType:       FrameTypeHello,
			wantWayfarerID: "wayfarer-a",
			wantDeviceID:   "device-1",
		},
		{
			name:     "send",
			raw:      `{"type":"send","to":"wayfarer-b","payload_b64":"dGVzdA==","ttl_seconds":60}`,
			wantType: FrameTypeSend,
			wantTo:   "wayfarer-b",
		},
		{
			name:      "ack",
			raw:       `{"type":"ack","msg_id":"msg-1"}`,
			wantType:  FrameTypeAck,
			wantMsgID: "msg-1",
		},
		{
			name:     "pull",
			raw:      `{"type":"pull","limit":25}`,
			wantType: FrameTypePull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var frame WSFrame
			if err := json.Unmarshal([]byte(tt.raw), &frame); err != nil {
				t.Fatalf("decode frame: %v", err)
			}

			if frame.Type != tt.wantType {
				t.Fatalf("type mismatch: got %q want %q", frame.Type, tt.wantType)
			}
			if tt.wantWayfarerID != "" && frame.WayfarerID != tt.wantWayfarerID {
				t.Fatalf("wayfarer_id mismatch: got %q want %q", frame.WayfarerID, tt.wantWayfarerID)
			}
			if tt.wantDeviceID != "" && frame.DeviceID != tt.wantDeviceID {
				t.Fatalf("device_id mismatch: got %q want %q", frame.DeviceID, tt.wantDeviceID)
			}
			if tt.wantTo != "" && frame.To != tt.wantTo {
				t.Fatalf("to mismatch: got %q want %q", frame.To, tt.wantTo)
			}
			if tt.wantMsgID != "" && frame.MsgID != tt.wantMsgID {
				t.Fatalf("msg_id mismatch: got %q want %q", frame.MsgID, tt.wantMsgID)
			}

			encoded, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("encode frame: %v", err)
			}

			var roundTrip struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatalf("decode encoded frame: %v", err)
			}
			if roundTrip.Type != tt.wantType {
				t.Fatalf("round-trip type mismatch: got %q want %q", roundTrip.Type, tt.wantType)
			}
		})
	}
}

func TestFederationFrameDecodeEncode(t *testing.T) {
	t.Skip("legacy relay federation frame set removed in Gossip V1 migration")
}

func TestRelayEnvelopeFrameRequiredEnvelopeFields(t *testing.T) {
	t.Skip("legacy relay envelope frame set removed in Gossip V1 migration")
}
