package model

import (
	"encoding/json"
	"testing"
	"time"
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
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		raw       string
		wantType  string
		decodeRun func(t *testing.T, payload []byte)
	}{
		{
			name:     "relay_hello",
			raw:      `{"type":"relay_hello","relay_id":"relay-1","version":"1.0"}`,
			wantType: FrameTypeRelayHello,
			decodeRun: func(t *testing.T, payload []byte) {
				var frame RelayHelloFrame
				if err := json.Unmarshal(payload, &frame); err != nil {
					t.Fatalf("decode relay_hello: %v", err)
				}
				if frame.RelayID == "" || frame.Version == "" {
					t.Fatalf("relay_hello missing required fields: %+v", frame)
				}
				assertRoundTripType(t, frame, FrameTypeRelayHello)
			},
		},
		{
			name: "relay_forward",
			raw: `{"type":"relay_forward","message":{"msg_id":"msg-1","from":"alice","to":"bob","payload_b64":"dGVzdA==","at":"` +
				now.Format(time.RFC3339Nano) + `","expires_at":"` + now.Add(time.Hour).Format(time.RFC3339Nano) + `"}}`,
			wantType: FrameTypeRelayForward,
			decodeRun: func(t *testing.T, payload []byte) {
				var frame RelayForwardFrame
				if err := json.Unmarshal(payload, &frame); err != nil {
					t.Fatalf("decode relay_forward: %v", err)
				}
				if frame.Message == nil || frame.Message.ID == "" || frame.Message.To == "" {
					t.Fatalf("relay_forward missing required message fields: %+v", frame.Message)
				}
				assertRoundTripType(t, frame, FrameTypeRelayForward)
			},
		},
		{
			name: "relay_forward with envelope timestamps",
			raw: `{"type":"relay_forward","message":{"msg_id":"msg-1","from":"alice","to":"bob","payload_b64":"dGVzdA==","at":"` +
				now.Format(time.RFC3339Nano) + `","expires_at":"` + now.Add(time.Hour).Format(time.RFC3339Nano) + `"},"envelope":{"created_at":1704067200000,"expires_at":1704070800000}}`,
			wantType: FrameTypeRelayForward,
			decodeRun: func(t *testing.T, payload []byte) {
				var frame RelayForwardFrame
				if err := json.Unmarshal(payload, &frame); err != nil {
					t.Fatalf("decode relay_forward with envelope: %v", err)
				}
				if frame.Envelope == nil {
					t.Fatal("relay_forward missing envelope timestamps")
				}
				if frame.Envelope.CreatedAt != 1704067200000 {
					t.Fatalf("created_at mismatch: got %d", frame.Envelope.CreatedAt)
				}
				if frame.Envelope.ExpiresAt != 1704070800000 {
					t.Fatalf("expires_at mismatch: got %d", frame.Envelope.ExpiresAt)
				}
				assertRoundTripType(t, frame, FrameTypeRelayForward)
			},
		},
		{
			name:     "relay_ack",
			raw:      `{"type":"relay_ack","envelope_id":"env-1","destination":"wayfarer-b","status":"accepted"}`,
			wantType: FrameTypeRelayAck,
			decodeRun: func(t *testing.T, payload []byte) {
				var frame RelayAckFrame
				if err := json.Unmarshal(payload, &frame); err != nil {
					t.Fatalf("decode relay_ack: %v", err)
				}
				if frame.EnvelopeID == "" || frame.Destination == "" || frame.Status == "" {
					t.Fatalf("relay_ack missing required fields: %+v", frame)
				}
				assertRoundTripType(t, frame, FrameTypeRelayAck)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(tt.raw)

			var frameType struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(payload, &frameType); err != nil {
				t.Fatalf("decode frame type: %v", err)
			}
			if frameType.Type != tt.wantType {
				t.Fatalf("frame type mismatch: got %q want %q", frameType.Type, tt.wantType)
			}

			tt.decodeRun(t, payload)
		})
	}
}

func TestRelayEnvelopeFrameRequiredEnvelopeFields(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		frame   RelayEnvelopeFrame
		wantErr bool
	}{
		{
			name: "missing envelope",
			frame: RelayEnvelopeFrame{
				Type: FrameTypeRelayForward,
			},
			wantErr: true,
		},
		{
			name: "missing envelope id",
			frame: RelayEnvelopeFrame{
				Type: FrameTypeRelayForward,
				Envelope: &Envelope{
					DestinationID: "wayfarer-b",
					CreatedAt:     now,
					ExpiresAt:     now.Add(time.Hour),
				},
			},
			wantErr: true,
		},
		{
			name: "missing envelope destination",
			frame: RelayEnvelopeFrame{
				Type: FrameTypeRelayForward,
				Envelope: &Envelope{
					ID:        "env-1",
					CreatedAt: now,
					ExpiresAt: now.Add(time.Hour),
				},
			},
			wantErr: true,
		},
		{
			name: "valid envelope",
			frame: RelayEnvelopeFrame{
				Type: FrameTypeRelayForward,
				Envelope: &Envelope{
					ID:            "env-1",
					DestinationID: "wayfarer-b",
					CreatedAt:     now,
					ExpiresAt:     now.Add(time.Hour),
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.frame.Envelope == nil {
				if !tt.wantErr {
					t.Fatal("expected envelope to be present")
				}
				return
			}

			err := tt.frame.Envelope.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("envelope validation mismatch: err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
