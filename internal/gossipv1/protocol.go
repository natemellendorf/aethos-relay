package gossipv1

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

const (
	GossipVersion       uint64 = 1
	MaxFrameBytes              = 1 << 20 // 1 MiB
	MaxSummaryItems     uint64 = 256
	MaxWantItems        uint64 = 256
	MaxTransferItems    uint64 = 32
	MaxReceiptItems     uint64 = MaxTransferItems
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

type DecodedEnvelope struct {
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

type SummaryPayload struct {
	Have []string `cbor:"have"`
}

type RequestPayload struct {
	Want []string `cbor:"want"`
}

type TransferObject struct {
	ID         string `cbor:"id"`
	From       string `cbor:"from"`
	To         string `cbor:"to"`
	PayloadB64 string `cbor:"payload_b64"`
	CreatedAt  int64  `cbor:"created_at"`
	ExpiresAt  int64  `cbor:"expires_at"`
}

type TransferPayload struct {
	Objects []TransferObject `cbor:"objects"`
}

type TransferObjectRejection struct {
	Index  uint64 `cbor:"index"`
	ID     string `cbor:"id,omitempty"`
	Reason string `cbor:"reason"`
}

type ParsedTransferPayload struct {
	Objects  []IndexedTransferObject
	Rejected []TransferObjectRejection
}

type IndexedTransferObject struct {
	Index  uint64
	Object TransferObject
}

type ReceiptPayload struct {
	Accepted []string                  `cbor:"accepted"`
	Rejected []TransferObjectRejection `cbor:"rejected,omitempty"`
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

func DecodeEnvelope(frame []byte) (DecodedEnvelope, error) {
	if len(frame) == 0 {
		return DecodedEnvelope{}, fmt.Errorf("gossipv1: empty frame")
	}
	if len(frame) > MaxFrameBytes {
		return DecodedEnvelope{}, fmt.Errorf("gossipv1: frame exceeds max bytes: %d > %d", len(frame), MaxFrameBytes)
	}

	var envelope DecodedEnvelope
	if err := cbor.Unmarshal(frame, &envelope); err != nil {
		return DecodedEnvelope{}, fmt.Errorf("gossipv1: decode envelope: %w", err)
	}
	if envelope.Type == "" {
		return DecodedEnvelope{}, fmt.Errorf("gossipv1: missing frame type")
	}
	if envelope.Payload == nil {
		return DecodedEnvelope{}, fmt.Errorf("gossipv1: missing payload")
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

func ParseSummaryPayload(payload map[string]any) (SummaryPayload, error) {
	if payload == nil {
		return SummaryPayload{}, fmt.Errorf("gossipv1: summary payload is required")
	}

	allowed := map[string]struct{}{
		"have": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return SummaryPayload{}, fmt.Errorf("gossipv1: unknown summary payload field %q", key)
		}
	}

	have, err := parseStringSlice(payload, "have")
	if err != nil {
		return SummaryPayload{}, err
	}
	have = dedupeStrings(have)
	if uint64(len(have)) > MaxSummaryItems {
		return SummaryPayload{}, fmt.Errorf("gossipv1: summary have exceeds limit: %d > %d", len(have), MaxSummaryItems)
	}

	return SummaryPayload{Have: have}, nil
}

func ParseRequestPayload(payload map[string]any) (RequestPayload, error) {
	if payload == nil {
		return RequestPayload{}, fmt.Errorf("gossipv1: request payload is required")
	}

	allowed := map[string]struct{}{
		"want": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return RequestPayload{}, fmt.Errorf("gossipv1: unknown request payload field %q", key)
		}
	}

	want, err := parseStringSlice(payload, "want")
	if err != nil {
		return RequestPayload{}, err
	}
	want = dedupeStrings(want)
	if uint64(len(want)) > MaxWantItems {
		return RequestPayload{}, fmt.Errorf("gossipv1: request want exceeds limit: %d > %d", len(want), MaxWantItems)
	}

	return RequestPayload{Want: want}, nil
}

func ParseTransferPayloadMixed(payload map[string]any) (ParsedTransferPayload, error) {
	if payload == nil {
		return ParsedTransferPayload{}, fmt.Errorf("gossipv1: transfer payload is required")
	}

	allowed := map[string]struct{}{
		"objects": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return ParsedTransferPayload{}, fmt.Errorf("gossipv1: unknown transfer payload field %q", key)
		}
	}

	rawObjects, ok := payload["objects"]
	if !ok {
		return ParsedTransferPayload{}, fmt.Errorf("gossipv1: missing objects")
	}

	objectsAny, ok := coerceAnySlice(rawObjects)
	if !ok {
		return ParsedTransferPayload{}, fmt.Errorf("gossipv1: objects must be array")
	}
	if uint64(len(objectsAny)) > MaxTransferItems {
		return ParsedTransferPayload{}, fmt.Errorf("gossipv1: transfer objects exceeds limit: %d > %d", len(objectsAny), MaxTransferItems)
	}

	parsed := ParsedTransferPayload{
		Objects:  make([]IndexedTransferObject, 0, len(objectsAny)),
		Rejected: make([]TransferObjectRejection, 0),
	}

	seenIDs := make(map[string]struct{}, len(objectsAny))
	for idx, candidate := range objectsAny {
		objMap, ok := coerceStringMap(candidate)
		if !ok {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				Reason: "object must be map",
			})
			continue
		}

		obj, err := parseTransferObject(objMap)
		if err != nil {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				Reason: err.Error(),
			})
			continue
		}

		if _, exists := seenIDs[obj.ID]; exists {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				ID:     obj.ID,
				Reason: "duplicate id in transfer frame",
			})
			continue
		}

		seenIDs[obj.ID] = struct{}{}
		parsed.Objects = append(parsed.Objects, IndexedTransferObject{Index: uint64(idx), Object: obj})
	}

	return parsed, nil
}

func ParseReceiptPayload(payload map[string]any) (ReceiptPayload, error) {
	if payload == nil {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt payload is required")
	}

	allowed := map[string]struct{}{
		"accepted": {},
		"rejected": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return ReceiptPayload{}, fmt.Errorf("gossipv1: unknown receipt payload field %q", key)
		}
	}

	accepted, err := parseStringSlice(payload, "accepted")
	if err != nil {
		return ReceiptPayload{}, err
	}
	accepted = dedupeStrings(accepted)
	if uint64(len(accepted)) > MaxReceiptItems {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt accepted exceeds limit: %d > %d", len(accepted), MaxReceiptItems)
	}

	rejected, err := parseReceiptRejected(payload)
	if err != nil {
		return ReceiptPayload{}, err
	}
	if uint64(len(rejected)) > MaxReceiptItems {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt rejected exceeds limit: %d > %d", len(rejected), MaxReceiptItems)
	}

	return ReceiptPayload{Accepted: accepted, Rejected: rejected}, nil
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

	raw, ok := coerceAnySlice(value)
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

func parseInt64(payload map[string]any, key string) (int64, error) {
	value, ok := payload[key]
	if !ok {
		return 0, fmt.Errorf("gossipv1: missing %s", key)
	}

	switch typed := value.(type) {
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > uint64((1<<63)-1) {
			return 0, fmt.Errorf("gossipv1: %s out of int64 range", key)
		}
		return int64(typed), nil
	case uint:
		if uint64(typed) > uint64((1<<63)-1) {
			return 0, fmt.Errorf("gossipv1: %s out of int64 range", key)
		}
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("gossipv1: %s must be integer", key)
	}
}

func parseTransferObject(payload map[string]any) (TransferObject, error) {
	allowed := map[string]struct{}{
		"id":          {},
		"from":        {},
		"to":          {},
		"payload_b64": {},
		"created_at":  {},
		"expires_at":  {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return TransferObject{}, fmt.Errorf("unknown transfer object field %q", key)
		}
	}

	id, err := parseString(payload, "id")
	if err != nil {
		return TransferObject{}, err
	}
	from, err := parseString(payload, "from")
	if err != nil {
		return TransferObject{}, err
	}
	to, err := parseString(payload, "to")
	if err != nil {
		return TransferObject{}, err
	}
	payloadB64, err := parseString(payload, "payload_b64")
	if err != nil {
		return TransferObject{}, err
	}
	createdAt, err := parseInt64(payload, "created_at")
	if err != nil {
		return TransferObject{}, err
	}
	expiresAt, err := parseInt64(payload, "expires_at")
	if err != nil {
		return TransferObject{}, err
	}

	if createdAt <= 0 {
		return TransferObject{}, fmt.Errorf("created_at must be > 0")
	}
	if expiresAt <= createdAt {
		return TransferObject{}, fmt.Errorf("expires_at must be greater than created_at")
	}

	return TransferObject{
		ID:         id,
		From:       from,
		To:         to,
		PayloadB64: payloadB64,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
	}, nil
}

func parseReceiptRejected(payload map[string]any) ([]TransferObjectRejection, error) {
	raw, ok := payload["rejected"]
	if !ok {
		return nil, nil
	}

	items, ok := coerceAnySlice(raw)
	if !ok {
		return nil, fmt.Errorf("gossipv1: rejected must be array")
	}

	rejected := make([]TransferObjectRejection, 0, len(items))
	for idx, item := range items {
		mapped, ok := coerceStringMap(item)
		if !ok {
			return nil, fmt.Errorf("gossipv1: rejected[%d] must be map", idx)
		}

		allowed := map[string]struct{}{
			"index":  {},
			"id":     {},
			"reason": {},
		}
		for key := range mapped {
			if _, ok := allowed[key]; !ok {
				return nil, fmt.Errorf("gossipv1: unknown rejected field %q", key)
			}
		}

		reason, err := parseString(mapped, "reason")
		if err != nil {
			return nil, err
		}

		entry := TransferObjectRejection{Reason: reason}
		if value, exists := mapped["id"]; exists {
			id, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("gossipv1: rejected[%d].id must be string", idx)
			}
			entry.ID = id
		}
		if _, exists := mapped["index"]; exists {
			parsedIdx, err := parseUint(mapped, "index")
			if err != nil {
				return nil, err
			}
			entry.Index = parsedIdx
		}

		rejected = append(rejected, entry)
	}

	return rejected, nil
}

func dedupeStrings(input []string) []string {
	if len(input) <= 1 {
		return input
	}

	seen := make(map[string]struct{}, len(input))
	output := make([]string, 0, len(input))
	for _, value := range input {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}

	return output
}

func coerceAnySlice(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}

	if direct, ok := value.([]any); ok {
		return direct, true
	}

	ref := reflect.ValueOf(value)
	if ref.Kind() != reflect.Slice && ref.Kind() != reflect.Array {
		return nil, false
	}

	out := make([]any, 0, ref.Len())
	for i := 0; i < ref.Len(); i++ {
		out = append(out, ref.Index(i).Interface())
	}

	return out, true
}

func coerceStringMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}

	if direct, ok := value.(map[string]any); ok {
		return direct, true
	}

	ref := reflect.ValueOf(value)
	if ref.Kind() != reflect.Map {
		return nil, false
	}

	out := make(map[string]any, ref.Len())
	iter := ref.MapRange()
	for iter.Next() {
		keyValue := iter.Key().Interface()
		key, ok := keyValue.(string)
		if !ok {
			return nil, false
		}
		out[key] = iter.Value().Interface()
	}

	return out, true
}
