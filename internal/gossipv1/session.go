package gossipv1

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type EventType string

const (
	EventTypeHelloValidated EventType = "hello_validated"
	EventTypeRelayIngest    EventType = "relay_ingest"
	EventTypeUntrustedRelay EventType = "relay_ingest_untrusted"
	EventTypeFatal          EventType = "fatal"
	EventTypeIgnored        EventType = "ignored"
)

type Event struct {
	Type      EventType
	FrameType string
	Hello     *HelloPayload
	ItemIDs   []string
	Err       error
}

type SessionAdapter struct {
	buffer               bytes.Buffer
	localHello           HelloPayload
	authenticatedRelay   bool
	helloValidated       bool
	terminated           bool
	lastFrameType        string
	lastObserverError    error
	untrustedRelayIngest int
}

func NewSessionAdapter(localHello HelloPayload, authenticatedRelay bool) *SessionAdapter {
	return &SessionAdapter{
		localHello:         localHello,
		authenticatedRelay: authenticatedRelay,
	}
}

func (s *SessionAdapter) InitialHelloBytes() ([]byte, error) {
	if s.terminated {
		return nil, fmt.Errorf("gossipv1: session terminated")
	}

	return EncodeHelloFrame(s.localHello)
}

func (s *SessionAdapter) PushInbound(chunk []byte) []Event {
	if s.terminated {
		return nil
	}
	if len(chunk) == 0 {
		return nil
	}

	_, _ = s.buffer.Write(chunk)
	events := make([]Event, 0, 1)

	for {
		if s.buffer.Len() < 4 {
			return events
		}

		raw := s.buffer.Bytes()
		frameLen := binary.BigEndian.Uint32(raw[:4])
		if frameLen == 0 {
			s.terminated = true
			return append(events, Event{Type: EventTypeFatal, Err: fmt.Errorf("gossipv1: zero frame length")})
		}
		if frameLen > uint32(MaxFrameBytes) {
			s.terminated = true
			return append(events, Event{Type: EventTypeFatal, Err: fmt.Errorf("gossipv1: frame length exceeds max: %d", frameLen)})
		}
		required := int(4 + frameLen)
		if s.buffer.Len() < required {
			return events
		}

		frame := make([]byte, frameLen)
		copy(frame, raw[4:required])
		s.buffer.Next(required)

		envelope, err := DecodeEnvelope(frame)
		if err != nil {
			s.terminated = true
			return append(events, Event{Type: EventTypeFatal, Err: err})
		}

		s.lastFrameType = envelope.Type

		switch envelope.Type {
		case FrameTypeHello:
			hello, err := ParseHelloPayload(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			s.helloValidated = true
			events = append(events, Event{Type: EventTypeHelloValidated, FrameType: envelope.Type, Hello: &hello})
		case FrameTypeRelayIngest:
			relayIngest, err := ParseRelayIngestPayload(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			if !s.authenticatedRelay {
				s.untrustedRelayIngest++
				events = append(events, Event{Type: EventTypeUntrustedRelay, FrameType: envelope.Type, ItemIDs: relayIngest.ItemIDs})
				continue
			}
			events = append(events, Event{Type: EventTypeRelayIngest, FrameType: envelope.Type, ItemIDs: relayIngest.ItemIDs})
		case FrameTypeSummary, FrameTypeRequest, FrameTypeTransfer, FrameTypeReceipt:
			events = append(events, Event{Type: EventTypeIgnored, FrameType: envelope.Type})
		default:
			s.terminated = true
			return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: fmt.Errorf("gossipv1: unknown frame type %q", envelope.Type)})
		}
	}
}

func (s *SessionAdapter) ObserveNonFatal(err error) {
	if err == nil {
		return
	}
	s.lastObserverError = err
}

func (s *SessionAdapter) IsHealthy() bool {
	return !s.terminated && s.helloValidated
}

func (s *SessionAdapter) Terminated() bool {
	return s.terminated
}

func (s *SessionAdapter) LastFrameType() string {
	return s.lastFrameType
}

func (s *SessionAdapter) LastObserverError() error {
	return s.lastObserverError
}

func (s *SessionAdapter) UntrustedRelayIngestCount() int {
	return s.untrustedRelayIngest
}
