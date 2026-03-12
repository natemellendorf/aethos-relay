package gossipv1

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

const (
	GossipVersion       uint64 = 1
	MaxFrameBytes              = 1 << 20 // 1 MiB
	MaxWantItems        uint64 = 256
	MaxTransferItems    uint64 = 32
	MaxRelayIngestItems int    = 256

	FrameTypeHello       = "HELLO"
	FrameTypeSummary     = "SUMMARY"
	FrameTypeRequest     = "REQUEST"
	FrameTypeTransfer    = "TRANSFER"
	FrameTypeReceipt     = "RECEIPT"
	FrameTypeRelayIngest = "RELAY_INGEST"
)

var canonicalEncMode = mustCanonicalEncMode()

func mustCanonicalEncMode() cbor.EncMode {
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("gossipv1: canonical cbor mode: %v", err))
	}
	return mode
}

type Envelope struct {
	Type    string `cbor:"type"`
	Payload any    `cbor:"payload"`
}

type decodedEnvelope struct {
	Type    string         `cbor:"type"`
	Payload map[string]any `cbor:"payload"`
}

type HelloPayload struct {
	Version          uint64   `cbor:"version"`
	NodeID           string   `cbor:"node_id"`
	NodePubKey       string   `cbor:"node_pubkey"`
	Capabilities     []string `cbor:"capabilities"`
	PropagationClass string   `cbor:"propagation_class"`
	MaxWant          uint64   `cbor:"max_want"`
	MaxTransfer      uint64   `cbor:"max_transfer"`
}

type RelayIngestPayload struct {
	ItemIDs []string `cbor:"item_ids"`
}

func BuildRelayHello(relayID string) HelloPayload {
	pubKey := sha256.Sum256([]byte("relay:" + relayID))
	nodeIDDigest := sha256.Sum256(pubKey[:])

	return HelloPayload{
		Version:          GossipVersion,
		NodeID:           hex.EncodeToString(nodeIDDigest[:]),
		NodePubKey:       base64.RawURLEncoding.EncodeToString(pubKey[:]),
		Capabilities:     []string{"relay"},
		PropagationClass: "relay",
		MaxWant:          MaxWantItems,
		MaxTransfer:      MaxTransferItems,
	}
}

func EncodeEnvelope(frameType string, payload any) ([]byte, error) {
	if frameType == "" {
		return nil, fmt.Errorf("gossipv1: frame type is required")
	}

	encoded, err := canonicalEncMode.Marshal(Envelope{Type: frameType, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("gossipv1: encode envelope: %w", err)
	}
	if len(encoded) > MaxFrameBytes {
		return nil, fmt.Errorf("gossipv1: frame exceeds max bytes: %d > %d", len(encoded), MaxFrameBytes)
	}

	return encoded, nil
}

func EncodeLengthPrefixed(frame []byte) ([]byte, error) {
	if len(frame) == 0 {
		return nil, fmt.Errorf("gossipv1: empty frame")
	}
	if len(frame) > MaxFrameBytes {
		return nil, fmt.Errorf("gossipv1: frame exceeds max bytes: %d > %d", len(frame), MaxFrameBytes)
	}

	out := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(out[:4], uint32(len(frame)))
	copy(out[4:], frame)
	return out, nil
}

func EncodeHelloFrame(local HelloPayload) ([]byte, error) {
	frame, err := EncodeEnvelope(FrameTypeHello, local)
	if err != nil {
		return nil, err
	}

	return EncodeLengthPrefixed(frame)
}

func DecodeEnvelope(frame []byte) (decodedEnvelope, error) {
	if len(frame) == 0 {
		return decodedEnvelope{}, fmt.Errorf("gossipv1: empty frame")
	}
	if len(frame) > MaxFrameBytes {
		return decodedEnvelope{}, fmt.Errorf("gossipv1: frame exceeds max bytes: %d > %d", len(frame), MaxFrameBytes)
	}

	var envelope decodedEnvelope
	if err := cbor.Unmarshal(frame, &envelope); err != nil {
		return decodedEnvelope{}, fmt.Errorf("gossipv1: decode envelope: %w", err)
	}
	if envelope.Type == "" {
		return decodedEnvelope{}, fmt.Errorf("gossipv1: missing frame type")
	}
	if envelope.Payload == nil {
		return decodedEnvelope{}, fmt.Errorf("gossipv1: missing payload")
	}

	return envelope, nil
}

func ParseHelloPayload(payload map[string]any) (HelloPayload, error) {
	if payload == nil {
		return HelloPayload{}, fmt.Errorf("gossipv1: hello payload is required")
	}

	allowed := map[string]struct{}{
		"version":           {},
		"node_id":           {},
		"node_pubkey":       {},
		"capabilities":      {},
		"propagation_class": {},
		"max_want":          {},
		"max_transfer":      {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return HelloPayload{}, fmt.Errorf("gossipv1: unknown hello payload field %q", key)
		}
	}

	version, err := parseUint(payload, "version")
	if err != nil {
		return HelloPayload{}, err
	}
	nodeID, err := parseString(payload, "node_id")
	if err != nil {
		return HelloPayload{}, err
	}
	nodePubKey, err := parseString(payload, "node_pubkey")
	if err != nil {
		return HelloPayload{}, err
	}
	capabilities, err := parseStringSlice(payload, "capabilities")
	if err != nil {
		return HelloPayload{}, err
	}
	propagationClass, err := parseString(payload, "propagation_class")
	if err != nil {
		return HelloPayload{}, err
	}
	maxWant, err := parseUint(payload, "max_want")
	if err != nil {
		return HelloPayload{}, err
	}
	maxTransfer, err := parseUint(payload, "max_transfer")
	if err != nil {
		return HelloPayload{}, err
	}

	hello := HelloPayload{
		Version:          version,
		NodeID:           nodeID,
		NodePubKey:       nodePubKey,
		Capabilities:     capabilities,
		PropagationClass: propagationClass,
		MaxWant:          maxWant,
		MaxTransfer:      maxTransfer,
	}

	if err := ValidateHello(hello); err != nil {
		return HelloPayload{}, err
	}

	return hello, nil
}

func ParseRelayIngestPayload(payload map[string]any) (RelayIngestPayload, error) {
	if payload == nil {
		return RelayIngestPayload{}, fmt.Errorf("gossipv1: relay_ingest payload is required")
	}

	allowed := map[string]struct{}{
		"item_ids": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return RelayIngestPayload{}, fmt.Errorf("gossipv1: unknown relay_ingest payload field %q", key)
		}
	}

	itemIDs, err := parseStringSlice(payload, "item_ids")
	if err != nil {
		return RelayIngestPayload{}, err
	}
	if len(itemIDs) > MaxRelayIngestItems {
		return RelayIngestPayload{}, fmt.Errorf("gossipv1: relay_ingest item_ids exceeds limit: %d > %d", len(itemIDs), MaxRelayIngestItems)
	}

	return RelayIngestPayload{ItemIDs: itemIDs}, nil
}

func ValidateHello(hello HelloPayload) error {
	if hello.Version != GossipVersion {
		return fmt.Errorf("gossipv1: version mismatch: got %d want %d", hello.Version, GossipVersion)
	}
	if len(hello.NodeID) != 64 {
		return fmt.Errorf("gossipv1: node_id must be 64 lowercase hex chars")
	}
	if hello.NodeID != strings.ToLower(hello.NodeID) {
		return fmt.Errorf("gossipv1: node_id must be lowercase hex")
	}

	pubKey, err := base64.RawURLEncoding.DecodeString(hello.NodePubKey)
	if err != nil {
		return fmt.Errorf("gossipv1: invalid node_pubkey: %w", err)
	}

	nodeIDDigest := sha256.Sum256(pubKey)
	expectedNodeID := hex.EncodeToString(nodeIDDigest[:])
	if hello.NodeID != expectedNodeID {
		return fmt.Errorf("gossipv1: node_id mismatch for node_pubkey")
	}

	if hello.MaxWant == 0 || hello.MaxWant > MaxWantItems {
		return fmt.Errorf("gossipv1: max_want out of range: %d", hello.MaxWant)
	}
	if hello.MaxTransfer == 0 || hello.MaxTransfer > MaxTransferItems {
		return fmt.Errorf("gossipv1: max_transfer out of range: %d", hello.MaxTransfer)
	}

	return nil
}

func parseString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key]
	if !ok {
		return "", fmt.Errorf("gossipv1: missing %s", key)
	}
	parsed, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("gossipv1: %s must be string", key)
	}
	if parsed == "" {
		return "", fmt.Errorf("gossipv1: %s must be non-empty", key)
	}
	return parsed, nil
}

func parseStringSlice(payload map[string]any, key string) ([]string, error) {
	value, ok := payload[key]
	if !ok {
		return nil, fmt.Errorf("gossipv1: missing %s", key)
	}

	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("gossipv1: %s must be array", key)
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("gossipv1: %s entries must be strings", key)
		}
		if s == "" {
			return nil, fmt.Errorf("gossipv1: %s entries must be non-empty", key)
		}
		out = append(out, s)
	}

	return out, nil
}

func parseUint(payload map[string]any, key string) (uint64, error) {
	value, ok := payload[key]
	if !ok {
		return 0, fmt.Errorf("gossipv1: missing %s", key)
	}

	switch typed := value.(type) {
	case uint8:
		return uint64(typed), nil
	case uint16:
		return uint64(typed), nil
	case uint32:
		return uint64(typed), nil
	case uint64:
		return typed, nil
	case uint:
		return uint64(typed), nil
	case int8:
		if typed < 0 {
			return 0, fmt.Errorf("gossipv1: %s must be unsigned", key)
		}
		return uint64(typed), nil
	case int16:
		if typed < 0 {
			return 0, fmt.Errorf("gossipv1: %s must be unsigned", key)
		}
		return uint64(typed), nil
	case int32:
		if typed < 0 {
			return 0, fmt.Errorf("gossipv1: %s must be unsigned", key)
		}
		return uint64(typed), nil
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("gossipv1: %s must be unsigned", key)
		}
		return uint64(typed), nil
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("gossipv1: %s must be unsigned", key)
		}
		return uint64(typed), nil
	default:
		return 0, fmt.Errorf("gossipv1: %s must be unsigned integer", key)
	}
}
