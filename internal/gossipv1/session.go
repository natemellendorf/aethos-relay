package gossipv1

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type EventType string

const (
	EventTypeHelloValidated EventType = "hello_validated"
	EventTypeSummary        EventType = "summary"
	EventTypeRequest        EventType = "request"
	EventTypeTransfer       EventType = "transfer"
	EventTypeReceipt        EventType = "receipt"
	EventTypeRelayIngest    EventType = "relay_ingest"
	EventTypeUntrustedRelay EventType = "relay_ingest_untrusted"
	EventTypeFatal          EventType = "fatal"
	EventTypeIgnored        EventType = "ignored"
)

type Event struct {
	Type      EventType
	FrameType string
	Hello     *HelloPayload
	Summary   *SummaryPayload
	Request   *RequestPayload
	Transfer  *ParsedTransferPayload
	Receipt   *ReceiptPayload
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
	awaitingReceipt      bool
	expectedReceiptOrder []string
	expectedReceiptIndex map[string]uint64
}

func NewSessionAdapter(localHello HelloPayload, authenticatedRelay bool) *SessionAdapter {
	return &SessionAdapter{
		localHello:           localHello,
		authenticatedRelay:   authenticatedRelay,
		expectedReceiptIndex: make(map[string]uint64),
	}
}

func (s *SessionAdapter) SetExpectedReceipt(ids []string) {
	s.awaitingReceipt = true
	s.expectedReceiptOrder = make([]string, 0, len(ids))
	s.expectedReceiptIndex = make(map[string]uint64, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := s.expectedReceiptIndex[id]; exists {
			continue
		}
		s.expectedReceiptIndex[id] = uint64(len(s.expectedReceiptOrder))
		s.expectedReceiptOrder = append(s.expectedReceiptOrder, id)
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
		case FrameTypeSummary:
			summary, err := ParseSummaryPayload(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			events = append(events, Event{Type: EventTypeSummary, FrameType: envelope.Type, Summary: &summary})
		case FrameTypeRequest:
			request, err := ParseRequestPayload(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			events = append(events, Event{Type: EventTypeRequest, FrameType: envelope.Type, Request: &request})
		case FrameTypeTransfer:
			transfer, err := ParseTransferPayloadMixed(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			events = append(events, Event{Type: EventTypeTransfer, FrameType: envelope.Type, Transfer: &transfer})
		case FrameTypeReceipt:
			receipt, err := ParseReceiptPayload(envelope.Payload)
			if err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			if err := s.validateExpectedReceipt(receipt); err != nil {
				s.terminated = true
				return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: err})
			}
			s.awaitingReceipt = false
			s.expectedReceiptOrder = nil
			s.expectedReceiptIndex = make(map[string]uint64)
			events = append(events, Event{Type: EventTypeReceipt, FrameType: envelope.Type, Receipt: &receipt})
		default:
			s.terminated = true
			return append(events, Event{Type: EventTypeFatal, FrameType: envelope.Type, Err: fmt.Errorf("gossipv1: unknown frame type %q", envelope.Type)})
		}
	}
}

func (s *SessionAdapter) validateExpectedReceipt(receipt ReceiptPayload) error {
	if !s.awaitingReceipt {
		return fmt.Errorf("gossipv1: unexpected receipt without pending transfer")
	}

	if len(s.expectedReceiptOrder) == 0 {
		if len(receipt.Accepted) == 0 && len(receipt.Rejected) == 0 {
			return nil
		}
		return fmt.Errorf("gossipv1: receipt acknowledged items but pending transfer is empty")
	}

	acknowledged := make(map[string]struct{}, len(s.expectedReceiptOrder))

	for _, acceptedID := range receipt.Accepted {
		if _, ok := s.expectedReceiptIndex[acceptedID]; !ok {
			return fmt.Errorf("gossipv1: receipt accepted id %q not present in pending transfer", acceptedID)
		}
		if _, duplicate := acknowledged[acceptedID]; duplicate {
			return fmt.Errorf("gossipv1: duplicate acknowledgement for id %q", acceptedID)
		}
		acknowledged[acceptedID] = struct{}{}
	}

	for _, rejected := range receipt.Rejected {
		resolvedByIndex := ""
		if rejected.HasIndex {
			if rejected.Index >= uint64(len(s.expectedReceiptOrder)) {
				return fmt.Errorf("gossipv1: receipt rejected index %d out of bounds for pending transfer size %d", rejected.Index, len(s.expectedReceiptOrder))
			}
			resolvedByIndex = s.expectedReceiptOrder[rejected.Index]
		}

		resolvedID := rejected.ID
		if resolvedID == "" {
			if !rejected.HasIndex {
				return fmt.Errorf("gossipv1: receipt rejected entry must include id or index")
			}
			resolvedID = resolvedByIndex
		} else if _, ok := s.expectedReceiptIndex[resolvedID]; !ok {
			return fmt.Errorf("gossipv1: receipt rejected id %q not present in pending transfer", resolvedID)
		}

		if rejected.HasIndex && rejected.ID != "" && resolvedByIndex != rejected.ID {
			return fmt.Errorf("gossipv1: receipt rejected id %q does not match index %d (%q)", rejected.ID, rejected.Index, resolvedByIndex)
		}

		if _, duplicate := acknowledged[resolvedID]; duplicate {
			return fmt.Errorf("gossipv1: duplicate acknowledgement for id %q", resolvedID)
		}
		acknowledged[resolvedID] = struct{}{}
	}

	if len(acknowledged) != len(s.expectedReceiptOrder) {
		return fmt.Errorf("gossipv1: receipt coverage mismatch: got %d acknowledgements for %d pending items", len(acknowledged), len(s.expectedReceiptOrder))
	}

	return nil
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
