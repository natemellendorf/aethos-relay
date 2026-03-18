package gossipv1

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

const (
	GossipVersion          uint64 = 1
	MaxFrameBytes                 = 1 << 20 // 1 MiB
	BloomFilterBytes              = 2048
	BloomHashCount                = 4
	MaxSummaryItems        uint64 = 256
	MaxSummaryPreviewItems uint64 = 64
	MaxWantItems           uint64 = 256
	MaxTransferItems       uint64 = 32
	MaxTransferBytes       uint64 = 524288
	MaxReceiptItems        uint64 = MaxTransferItems
	MaxRelayIngestItems    int    = 256
	DigestHexBytes                = 32
	DigestHexLen                  = DigestHexBytes * 2

	FrameTypeHello       = "HELLO"
	FrameTypeSummary     = "SUMMARY"
	FrameTypeRequest     = "REQUEST"
	FrameTypeTransfer    = "TRANSFER"
	FrameTypeReceipt     = "RECEIPT"
	FrameTypeRelayIngest = "RELAY_INGEST"

	envelopeSignatureDomain = "AETHOS_ENVELOPE_V1"
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
	BloomFilter    []byte   `cbor:"bloom_filter"`
	ItemCount      uint64   `cbor:"item_count"`
	PreviewItemIDs []string `cbor:"preview_item_ids,omitempty"`
	PreviewCursor  string   `cbor:"preview_cursor,omitempty"`
	Have           []string `cbor:"-"`
}

type RequestPayload struct {
	Want []string `cbor:"want"`
}

type TransferObject struct {
	ItemID       string       `cbor:"item_id"`
	EnvelopeB64  string       `cbor:"envelope_b64"`
	ExpiryUnixMS uint64       `cbor:"expiry_unix_ms"`
	HopCount     uint64       `cbor:"hop_count"`
	Envelope     ItemEnvelope `cbor:"-"`
}

type TransferPayload struct {
	Objects []TransferObject `cbor:"objects"`
}

type TransferObjectRejection struct {
	Index      uint64
	ID         string
	Reason     string
	Detail     string
	Diagnostic *TransferObjectDiagnostics
}

type TransferObjectDiagnostics struct {
	Base64URLDecodeOK         bool
	DecodedByteLen            int
	DecodedFirstByteHex       string
	DecodedBytesDigest        string
	CBORDecodeOK              bool
	CanonicalReencodeMatch    bool
	CanonicalReencodeDigest   string
	ItemIDMatchesDecodedBytes bool
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
	Accepted []string `cbor:"received"`
}

type canonicalItemEnvelope struct {
	From       string `cbor:"from"`
	To         string `cbor:"to"`
	PayloadB64 string `cbor:"payload_b64"`
	CreatedAt  int64  `cbor:"created_at"`
	ExpiresAt  int64  `cbor:"expires_at"`
}

type canonicalTransferEnvelope struct {
	ToWayfarerID []byte `cbor:"to_wayfarer_id"`
	ManifestID   []byte `cbor:"manifest_id"`
	Body         []byte `cbor:"body"`
	AuthorPubKey []byte `cbor:"author_pubkey"`
	AuthorSig    []byte `cbor:"author_sig"`
}

type canonicalTransferSigningPayload struct {
	ToWayfarerID []byte `cbor:"to_wayfarer_id"`
	ManifestID   []byte `cbor:"manifest_id"`
	Body         []byte `cbor:"body"`
}

type ItemEnvelope struct {
	From       string
	To         string
	PayloadB64 string
	CreatedAt  int64
	ExpiresAt  int64
	ManifestID string
	AuthorSig  []byte
}

// IsDigestHexID reports whether value is a lowercase 64-hex digest ID.
func IsDigestHexID(value string) bool {
	return isDigestHex(value)
}

// NormalizeDigestHexIDs validates, dedupes, and bytewise-sorts digest IDs.
func NormalizeDigestHexIDs(input []string) ([]string, error) {
	return normalizeDigestHexIDs(input)
}

// SortDigestHexIDs bytewise-sorts digest IDs in place.
func SortDigestHexIDs(ids []string) {
	sortDigestHexIDs(ids)
}

// CompareDigestHexIDs compares lowercase digest hex IDs by decoded bytes.
// Returns -1 when left < right, 0 when equal, 1 when left > right.
func CompareDigestHexIDs(left string, right string) int {
	return compareDigestHex(left, right)
}

// ComputeItemID computes item_id as lowercase sha256(canonical envelope bytes).
func ComputeItemID(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) string {
	envelopeBytes := mustCanonicalItemEnvelopeBytes(from, to, payloadB64, createdAt, expiresAt)
	digest := sha256.Sum256(envelopeBytes)
	return hex.EncodeToString(digest[:])
}

// ComputeTransferObjectItemID computes the content-address item_id for transfer object fields.
func ComputeTransferObjectItemID(object TransferObject) string {
	envelopeBytes, err := decodeBase64URLRaw(object.EnvelopeB64)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(envelopeBytes)
	return hex.EncodeToString(digest[:])
}

func EncodeItemEnvelopeB64(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) (string, error) {
	envelope := canonicalItemEnvelope{From: from, To: to, PayloadB64: payloadB64, CreatedAt: createdAt, ExpiresAt: expiresAt}
	encoded, err := canonicalEncMode.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("gossipv1: encode item envelope: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeItemEnvelopeB64(envelopeB64 string) (ItemEnvelope, error) {
	envelopeBytes, err := decodeBase64URLRaw(envelopeB64)
	if err != nil {
		return ItemEnvelope{}, err
	}
	envelope, err := parseCanonicalItemEnvelope(envelopeBytes)
	if err != nil {
		return ItemEnvelope{}, err
	}
	return envelope, nil
}

func EncodeTransferEnvelopeB64(toWayfarerIDHex string, manifestIDHex string, bodyB64 string, authorPubKey []byte, authorSig []byte) (string, error) {
	toBytes, err := decodeDigestHexBytes(toWayfarerIDHex)
	if err != nil {
		return "", fmt.Errorf("gossipv1: invalid to_wayfarer_id: %w", err)
	}
	manifestBytes, err := decodeDigestHexBytes(manifestIDHex)
	if err != nil {
		return "", fmt.Errorf("gossipv1: invalid manifest_id: %w", err)
	}
	body, err := decodeBase64URLRaw(bodyB64)
	if err != nil {
		return "", fmt.Errorf("gossipv1: invalid envelope body: %w", err)
	}
	if len(authorPubKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("gossipv1: author_pubkey must be 32 bytes")
	}
	if len(authorSig) != ed25519.SignatureSize {
		return "", fmt.Errorf("gossipv1: author_sig must be 64 bytes")
	}

	envelope := canonicalTransferEnvelope{
		ToWayfarerID: toBytes,
		ManifestID:   manifestBytes,
		Body:         body,
		AuthorPubKey: append([]byte(nil), authorPubKey...),
		AuthorSig:    append([]byte(nil), authorSig...),
	}
	encoded, err := canonicalEncMode.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("gossipv1: encode transfer envelope: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func mustCanonicalItemEnvelopeBytes(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) []byte {
	envelope := canonicalItemEnvelope{
		From:       from,
		To:         to,
		PayloadB64: payloadB64,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
	}

	encoded, err := canonicalEncMode.Marshal(envelope)
	if err != nil {
		panic(fmt.Sprintf("gossipv1: canonical item envelope: %v", err))
	}

	return encoded
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

	bloomFilter, err := parseBytes(payload, "bloom_filter")
	if err != nil {
		return SummaryPayload{}, err
	}
	if len(bloomFilter) != BloomFilterBytes {
		return SummaryPayload{}, fmt.Errorf("gossipv1: summary bloom_filter must be %d bytes", BloomFilterBytes)
	}

	itemCount, err := parseUint(payload, "item_count")
	if err != nil {
		return SummaryPayload{}, err
	}

	previewItemIDs := []string{}
	if rawPreview, ok := payload["preview_item_ids"]; ok {
		previewItemIDs, err = parseStringSliceFromRaw(rawPreview, "preview_item_ids")
		if err != nil {
			return SummaryPayload{}, err
		}
		if uint64(len(previewItemIDs)) > MaxSummaryPreviewItems {
			return SummaryPayload{}, fmt.Errorf("gossipv1: summary preview_item_ids exceeds limit: %d > %d", len(previewItemIDs), MaxSummaryPreviewItems)
		}
		if err := validateSortedUniqueDigestIDs(previewItemIDs); err != nil {
			return SummaryPayload{}, err
		}
	}

	previewCursor, err := parseOptionalString(payload, "preview_cursor")
	if err != nil {
		return SummaryPayload{}, err
	}

	if len(previewItemIDs) == 0 {
		if previewCursor != "" {
			return SummaryPayload{}, fmt.Errorf("gossipv1: summary preview_cursor must be absent when preview_item_ids is empty")
		}
	} else {
		if previewCursor == "" {
			return SummaryPayload{}, fmt.Errorf("gossipv1: summary preview_cursor is required when preview_item_ids is present")
		}
		if !isDigestHex(previewCursor) {
			return SummaryPayload{}, fmt.Errorf("gossipv1: summary preview_cursor must be 64 lowercase hex chars")
		}
		if previewCursor != previewItemIDs[len(previewItemIDs)-1] {
			return SummaryPayload{}, fmt.Errorf("gossipv1: summary preview_cursor must equal last preview_item_ids element")
		}
	}

	return SummaryPayload{
		BloomFilter:    bloomFilter,
		ItemCount:      itemCount,
		PreviewItemIDs: previewItemIDs,
		PreviewCursor:  previewCursor,
		Have:           append([]string(nil), previewItemIDs...),
	}, nil
}

func BuildSummaryPayload(itemIDs []string) SummaryPayload {
	return BuildSummaryPreviewPayload(itemIDs, itemIDs, "")
}

func BuildSummaryPreviewPayload(itemIDs []string, previewItemIDs []string, previewCursor string) SummaryPayload {
	normalizedItemIDs := normalizeDigestHexIDsDropInvalid(itemIDs)
	normalizedPreviewItemIDs := normalizeDigestHexIDsDropInvalid(previewItemIDs)
	if uint64(len(normalizedPreviewItemIDs)) > MaxSummaryPreviewItems {
		normalizedPreviewItemIDs = normalizedPreviewItemIDs[:MaxSummaryPreviewItems]
	}
	bloomFilter := make([]byte, BloomFilterBytes)

	for _, itemID := range normalizedItemIDs {
		itemBytes := bloomFilterItemBytes(itemID)
		for hashIndex := 0; hashIndex < BloomHashCount; hashIndex++ {
			digest := sha256.Sum256(append(itemBytes, byte(hashIndex)))
			hashValue := binary.BigEndian.Uint64(digest[:8])
			bitIndex := hashValue % uint64(BloomFilterBytes*8)
			byteIndex := int(bitIndex / 8)
			bitOffset := uint(bitIndex % 8)
			bloomFilter[byteIndex] |= byte(1 << bitOffset)
		}
	}

	if len(normalizedPreviewItemIDs) == 0 {
		previewCursor = ""
	} else if previewCursor == "" || previewCursor != normalizedPreviewItemIDs[len(normalizedPreviewItemIDs)-1] {
		previewCursor = normalizedPreviewItemIDs[len(normalizedPreviewItemIDs)-1]
	}

	return SummaryPayload{
		BloomFilter:    bloomFilter,
		ItemCount:      uint64(len(normalizedItemIDs)),
		PreviewItemIDs: normalizedPreviewItemIDs,
		PreviewCursor:  previewCursor,
		Have:           append([]string(nil), normalizedPreviewItemIDs...),
	}
}

func BloomFilterMightContain(bloomFilter []byte, itemID string) bool {
	if len(bloomFilter) != BloomFilterBytes {
		return false
	}
	itemBytes, ok := digestBytes(itemID)
	if !ok {
		return false
	}

	for hashIndex := 0; hashIndex < BloomHashCount; hashIndex++ {
		digest := sha256.Sum256(append(itemBytes, byte(hashIndex)))
		hashValue := binary.BigEndian.Uint64(digest[:8])
		bitIndex := hashValue % uint64(BloomFilterBytes*8)
		byteIndex := int(bitIndex / 8)
		bitOffset := uint(bitIndex % 8)
		if bloomFilter[byteIndex]&(byte(1)<<bitOffset) == 0 {
			return false
		}
	}

	return true
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
	if uint64(len(want)) > MaxWantItems {
		return RequestPayload{}, fmt.Errorf("gossipv1: request want exceeds limit: %d > %d", len(want), MaxWantItems)
	}
	if err := validateSortedUniqueDigestIDsForField(want, "want"); err != nil {
		return RequestPayload{}, err
	}

	return RequestPayload{Want: append([]string(nil), want...)}, nil
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
	var transferBytes uint64
	for idx, candidate := range objectsAny {
		objMap, ok := coerceStringMap(candidate)
		if !ok {
			detail := "object must be map"
			if keys := mapKeys(candidate); len(keys) > 0 {
				detail = fmt.Sprintf("object must be map (keys=%s)", strings.Join(keys, ","))
			}
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				Reason: "unknown_parse_error",
				Detail: detail,
			})
			continue
		}

		obj, err := parseTransferObject(objMap)
		if err != nil {
			reason := "unknown_parse_error"
			detail := err.Error()
			var diagnostic *TransferObjectDiagnostics
			if parseErr, ok := err.(*transferParseError); ok {
				reason = parseErr.reason
				detail = parseErr.detail
				diagnostic = parseErr.diagnostic
			}
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:      uint64(idx),
				ID:         transferObjectItemIDFromMap(objMap),
				Reason:     reason,
				Detail:     detail,
				Diagnostic: diagnostic,
			})
			continue
		}

		decodedEnvelope, err := decodeBase64URLRaw(obj.EnvelopeB64)
		if err != nil {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				ID:     obj.ItemID,
				Reason: "invalid_envelope_encoding",
				Detail: err.Error(),
			})
			continue
		}
		transferBytes += uint64(len(decodedEnvelope))
		if transferBytes > MaxTransferBytes {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				ID:     obj.ItemID,
				Reason: "policy_reject",
				Detail: fmt.Sprintf("transfer envelope bytes exceeds limit: %d > %d", transferBytes, MaxTransferBytes),
			})
			continue
		}

		if _, exists := seenIDs[obj.ItemID]; exists {
			parsed.Rejected = append(parsed.Rejected, TransferObjectRejection{
				Index:  uint64(idx),
				ID:     obj.ItemID,
				Reason: "duplicate_item",
				Detail: "duplicate item_id in transfer frame",
			})
			continue
		}

		seenIDs[obj.ItemID] = struct{}{}
		parsed.Objects = append(parsed.Objects, IndexedTransferObject{Index: uint64(idx), Object: obj})
	}

	return parsed, nil
}

func ParseReceiptPayload(payload map[string]any) (ReceiptPayload, error) {
	if payload == nil {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt payload is required")
	}

	allowed := map[string]struct{}{
		"received": {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return ReceiptPayload{}, fmt.Errorf("gossipv1: unknown receipt payload field %q", key)
		}
	}

	received, err := parseStringSlice(payload, "received")
	if err != nil {
		return ReceiptPayload{}, err
	}
	if hasDuplicateStrings(received) {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt received contains duplicate ids")
	}
	if uint64(len(received)) > MaxReceiptItems {
		return ReceiptPayload{}, fmt.Errorf("gossipv1: receipt received exceeds limit: %d > %d", len(received), MaxReceiptItems)
	}

	return ReceiptPayload{Accepted: received}, nil
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

func parseOptionalString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key]
	if !ok {
		return "", nil
	}
	parsed, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("gossipv1: %s must be string", key)
	}
	return parsed, nil
}

func parseStringSlice(payload map[string]any, key string) ([]string, error) {
	value, ok := payload[key]
	if !ok {
		return nil, fmt.Errorf("gossipv1: missing %s", key)
	}
	return parseStringSliceFromRaw(value, key)
}

func parseStringSliceFromRaw(value any, key string) ([]string, error) {
	if value == nil {
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

func isDigestHex(value string) bool {
	_, ok := digestBytes(value)
	return ok
}

func digestHexLess(left string, right string) bool {
	return compareDigestHex(left, right) < 0
}

func compareDigestHex(left string, right string) int {
	leftBytes, leftOK := digestBytes(left)
	rightBytes, rightOK := digestBytes(right)
	if !leftOK || !rightOK {
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		default:
			return 0
		}
	}
	return bytes.Compare(leftBytes, rightBytes)
}

func sortDigestHexIDs(ids []string) {
	if len(ids) <= 1 {
		return
	}
	sort.Slice(ids, func(i, j int) bool {
		return digestHexLess(ids[i], ids[j])
	})
}

func normalizeDigestHexIDs(input []string) ([]string, error) {
	if len(input) == 0 {
		return []string{}, nil
	}

	seen := make(map[string]struct{}, len(input))
	ids := make([]string, 0, len(input))
	for _, candidate := range input {
		if !isDigestHex(candidate) {
			return nil, fmt.Errorf("gossipv1: item id must be 64 lowercase hex chars")
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		ids = append(ids, candidate)
	}

	sortDigestHexIDs(ids)
	return ids, nil
}

func validateSortedUniqueDigestIDs(input []string) error {
	return validateSortedUniqueDigestIDsForField(input, "preview_item_ids")
}

func validateSortedUniqueDigestIDsForField(input []string, fieldName string) error {
	if len(input) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(input))
	previous := ""
	for index, itemID := range input {
		if !isDigestHex(itemID) {
			return fmt.Errorf("gossipv1: item id must be 64 lowercase hex chars")
		}
		if _, exists := seen[itemID]; exists {
			return fmt.Errorf("gossipv1: %s entries must be unique", fieldName)
		}
		seen[itemID] = struct{}{}

		if index == 0 {
			previous = itemID
			continue
		}
		if compareDigestHex(previous, itemID) >= 0 {
			return fmt.Errorf("gossipv1: %s must be sorted by digest bytes", fieldName)
		}
		previous = itemID
	}

	return nil
}

func normalizeDigestHexIDsLossy(input []string) []string {
	normalized, err := normalizeDigestHexIDs(input)
	if err == nil {
		return normalized
	}

	fallback := dedupeStrings(input)
	if len(fallback) > 1 {
		sort.Strings(fallback)
	}
	return fallback
}

func normalizeDigestHexIDsDropInvalid(input []string) []string {
	if len(input) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(input))
	ids := make([]string, 0, len(input))
	for _, candidate := range input {
		if !isDigestHex(candidate) {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		ids = append(ids, candidate)
	}

	sortDigestHexIDs(ids)
	return ids
}

func parseBytes(payload map[string]any, key string) ([]byte, error) {
	value, ok := payload[key]
	if !ok {
		return nil, fmt.Errorf("gossipv1: missing %s", key)
	}

	switch typed := value.(type) {
	case []byte:
		out := make([]byte, len(typed))
		copy(out, typed)
		return out, nil
	}

	ref := reflect.ValueOf(value)
	if ref.Kind() != reflect.Slice && ref.Kind() != reflect.Array {
		return nil, fmt.Errorf("gossipv1: %s must be bytes", key)
	}

	out := make([]byte, ref.Len())
	for index := 0; index < ref.Len(); index++ {
		item := ref.Index(index)
		if item.Kind() != reflect.Uint8 {
			return nil, fmt.Errorf("gossipv1: %s must be bytes", key)
		}
		out[index] = uint8(item.Uint())
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
	diagnostic := &TransferObjectDiagnostics{}

	allowed := map[string]struct{}{
		"item_id":        {},
		"envelope_b64":   {},
		"expiry_unix_ms": {},
		"hop_count":      {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return TransferObject{}, newTransferParseError("unknown_parse_error", fmt.Sprintf("unknown transfer object field %q", key))
		}
	}

	itemID, err := parseString(payload, "item_id")
	if err != nil {
		return TransferObject{}, classifyTransferFieldError("item_id", err)
	}
	if !isDigestHex(itemID) {
		return TransferObject{}, newTransferParseError("invalid_item_id", "item_id must be 64 lowercase hex chars")
	}

	envelopeB64, err := parseString(payload, "envelope_b64")
	if err != nil {
		return TransferObject{}, classifyTransferFieldError("envelope_b64", err)
	}
	envelopeBytes, err := decodeBase64URLRaw(envelopeB64)
	if err != nil {
		return TransferObject{}, newTransferParseError("invalid_envelope_encoding", err.Error(), diagnostic)
	}
	diagnostic.Base64URLDecodeOK = true
	diagnostic.DecodedByteLen = len(envelopeBytes)
	if len(envelopeBytes) > 0 {
		diagnostic.DecodedFirstByteHex = fmt.Sprintf("%02x", envelopeBytes[0])
	}
	diagnostic.DecodedBytesDigest = shortDigestHex(envelopeBytes)

	decodedDigest := sha256.Sum256(envelopeBytes)
	if isDigestHex(itemID) {
		diagnostic.ItemIDMatchesDecodedBytes = itemID == hex.EncodeToString(decodedDigest[:])
	}

	var decodedEnvelope any
	if err := cbor.Unmarshal(envelopeBytes, &decodedEnvelope); err != nil {
		return TransferObject{}, newTransferParseError("invalid_envelope_encoding", fmt.Sprintf("envelope_b64 cbor decode failed: %v", err), diagnostic)
	}
	diagnostic.CBORDecodeOK = true
	reencodedEnvelope, err := canonicalEncMode.Marshal(decodedEnvelope)
	if err != nil {
		return TransferObject{}, newTransferParseError("invalid_envelope_encoding", fmt.Sprintf("envelope canonical re-encode failed: %v", err), diagnostic)
	}
	diagnostic.CanonicalReencodeMatch = bytes.Equal(envelopeBytes, reencodedEnvelope)
	if !diagnostic.CanonicalReencodeMatch {
		diagnostic.CanonicalReencodeDigest = shortDigestHex(reencodedEnvelope)
		return TransferObject{}, newTransferParseError("invalid_envelope_encoding", "envelope_b64 must decode to canonical CBOR bytes", diagnostic)
	}
	envelope, err := parseCanonicalItemEnvelope(envelopeBytes)
	if err != nil {
		return TransferObject{}, newTransferParseError("policy_reject", err.Error(), diagnostic)
	}

	expiryUnixMS, err := parseUint(payload, "expiry_unix_ms")
	if err != nil {
		return TransferObject{}, classifyTransferFieldError("expiry_unix_ms", err)
	}

	hopCount, err := parseUint(payload, "hop_count")
	if err != nil {
		return TransferObject{}, classifyTransferFieldError("hop_count", err)
	}
	if hopCount > 65535 {
		return TransferObject{}, newTransferParseError("invalid_hop_count", "hop_count must be <= 65535")
	}

	digest := sha256.Sum256(envelopeBytes)
	computedID := hex.EncodeToString(digest[:])
	if computedID != itemID {
		return TransferObject{}, newTransferParseError("invalid_item_id", "item_id must equal sha256(envelope bytes)", diagnostic)
	}

	return TransferObject{
		ItemID:       itemID,
		EnvelopeB64:  envelopeB64,
		ExpiryUnixMS: expiryUnixMS,
		HopCount:     hopCount,
		Envelope: ItemEnvelope{
			From:       envelope.From,
			To:         envelope.To,
			PayloadB64: envelope.PayloadB64,
			CreatedAt:  envelope.CreatedAt,
			ExpiresAt:  envelope.ExpiresAt,
		},
	}, nil
}

func shortDigestHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:6])
}

func transferObjectItemIDFromMap(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	raw, ok := payload["item_id"]
	if !ok {
		return ""
	}
	itemID, ok := raw.(string)
	if !ok {
		return ""
	}
	return itemID
}

func mapKeys(value any) []string {
	ref := reflect.ValueOf(value)
	if !ref.IsValid() || ref.Kind() != reflect.Map {
		return nil
	}
	keys := make([]string, 0, ref.Len())
	iter := ref.MapRange()
	for iter.Next() {
		key, ok := iter.Key().Interface().(string)
		if !ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decodeBase64URLRaw(value string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("envelope_b64 must be non-empty")
	}
	if strings.Contains(value, "=") {
		return nil, fmt.Errorf("envelope_b64 must be unpadded base64url")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("envelope_b64 must be unpadded base64url: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("envelope_b64 must decode to non-empty bytes")
	}
	return decoded, nil
}

func isCanonicalCBOR(encoded []byte) bool {
	var decoded any
	if err := cbor.Unmarshal(encoded, &decoded); err != nil {
		return false
	}
	reencoded, err := canonicalEncMode.Marshal(decoded)
	if err != nil {
		return false
	}
	return bytes.Equal(encoded, reencoded)
}

func parseCanonicalItemEnvelope(encoded []byte) (ItemEnvelope, error) {
	var payload map[string]any
	if err := cbor.Unmarshal(encoded, &payload); err != nil {
		return ItemEnvelope{}, fmt.Errorf("decode envelope bytes: %w", err)
	}
	allowed := map[string]struct{}{
		"to_wayfarer_id": {},
		"manifest_id":    {},
		"body":           {},
		"author_pubkey":  {},
		"author_sig":     {},
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return ItemEnvelope{}, fmt.Errorf("unknown envelope field %q", key)
		}
	}

	toWayfarerID, err := parseBytes(payload, "to_wayfarer_id")
	if err != nil {
		return ItemEnvelope{}, err
	}
	if len(toWayfarerID) != DigestHexBytes {
		return ItemEnvelope{}, fmt.Errorf("to_wayfarer_id must be 32 bytes")
	}
	manifestID, err := parseBytes(payload, "manifest_id")
	if err != nil {
		return ItemEnvelope{}, err
	}
	if len(manifestID) != DigestHexBytes {
		return ItemEnvelope{}, fmt.Errorf("manifest_id must be 32 bytes")
	}
	body, err := parseBytes(payload, "body")
	if err != nil {
		return ItemEnvelope{}, err
	}
	authorPubKey, err := parseBytes(payload, "author_pubkey")
	if err != nil {
		return ItemEnvelope{}, err
	}
	if len(authorPubKey) != ed25519.PublicKeySize {
		return ItemEnvelope{}, fmt.Errorf("author_pubkey must be 32 bytes")
	}
	authorSig, err := parseBytes(payload, "author_sig")
	if err != nil {
		return ItemEnvelope{}, err
	}
	if len(authorSig) != ed25519.SignatureSize {
		return ItemEnvelope{}, fmt.Errorf("author_sig must be 64 bytes")
	}

	signingPayload, err := canonicalEncMode.Marshal(canonicalTransferSigningPayload{
		ToWayfarerID: toWayfarerID,
		ManifestID:   manifestID,
		Body:         body,
	})
	if err != nil {
		return ItemEnvelope{}, fmt.Errorf("encode signing payload: %w", err)
	}

	domainInput := append([]byte(envelopeSignatureDomain), signingPayload...)
	signatureDigest := sha256.Sum256(domainInput)
	if !ed25519.Verify(ed25519.PublicKey(authorPubKey), signatureDigest[:], authorSig) {
		return ItemEnvelope{}, fmt.Errorf("invalid envelope signature")
	}

	authorID := sha256.Sum256(authorPubKey)

	return ItemEnvelope{
		From:       hex.EncodeToString(authorID[:]),
		To:         hex.EncodeToString(toWayfarerID),
		PayloadB64: base64.RawURLEncoding.EncodeToString(body),
		ManifestID: hex.EncodeToString(manifestID),
		AuthorSig:  append([]byte(nil), authorSig...),
	}, nil
}

func decodeDigestHexBytes(value string) ([]byte, error) {
	if !isDigestHex(value) {
		return nil, fmt.Errorf("digest must be lowercase 64-hex chars")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

type transferParseError struct {
	reason     string
	detail     string
	diagnostic *TransferObjectDiagnostics
}

func (e *transferParseError) Error() string {
	return e.detail
}

func newTransferParseError(reason string, detail string, diagnostic ...*TransferObjectDiagnostics) *transferParseError {
	var diag *TransferObjectDiagnostics
	if len(diagnostic) > 0 {
		diag = diagnostic[0]
	}
	return &transferParseError{reason: reason, detail: detail, diagnostic: diag}
}

func TransferObjectDiagnosticLogKV(diagnostic *TransferObjectDiagnostics) []any {
	if diagnostic == nil {
		diagnostic = &TransferObjectDiagnostics{}
	}
	return []any{
		"diag_base64url_decode_ok", diagnostic.Base64URLDecodeOK,
		"diag_decoded_byte_len", diagnostic.DecodedByteLen,
		"diag_decoded_first_byte_hex", diagnostic.DecodedFirstByteHex,
		"diag_decoded_bytes_digest", diagnostic.DecodedBytesDigest,
		"diag_cbor_decode_ok", diagnostic.CBORDecodeOK,
		"diag_canonical_reencode_match", diagnostic.CanonicalReencodeMatch,
		"diag_canonical_reencode_digest", diagnostic.CanonicalReencodeDigest,
		"diag_item_id_matches_decoded", diagnostic.ItemIDMatchesDecodedBytes,
	}
}

func classifyTransferFieldError(field string, err error) error {
	if strings.Contains(err.Error(), "missing") {
		switch field {
		case "item_id":
			return newTransferParseError("missing_item_id", err.Error())
		case "envelope_b64":
			return newTransferParseError("missing_envelope_b64", err.Error())
		case "expiry_unix_ms":
			return newTransferParseError("invalid_expiry_unix_ms", err.Error())
		case "hop_count":
			return newTransferParseError("invalid_hop_count", err.Error())
		}
	}
	switch field {
	case "item_id":
		return newTransferParseError("invalid_item_id", err.Error())
	case "envelope_b64":
		return newTransferParseError("invalid_envelope_encoding", err.Error())
	case "expiry_unix_ms":
		return newTransferParseError("invalid_expiry_unix_ms", err.Error())
	case "hop_count":
		return newTransferParseError("invalid_hop_count", err.Error())
	default:
		return newTransferParseError("unknown_parse_error", err.Error())
	}
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

func normalizeSummaryItemIDs(itemIDs []string) []string {
	if len(itemIDs) == 0 {
		return []string{}
	}

	normalized := make([]string, 0, len(itemIDs))
	seen := make(map[string]struct{}, len(itemIDs))
	for _, itemID := range itemIDs {
		if itemID == "" {
			continue
		}
		if _, exists := seen[itemID]; exists {
			continue
		}
		seen[itemID] = struct{}{}
		normalized = append(normalized, itemID)
	}

	return normalized
}

func bloomFilterItemBytes(itemID string) []byte {
	decoded, ok := digestBytes(itemID)
	if !ok {
		return nil
	}
	return decoded
}

func digestBytes(value string) ([]byte, bool) {
	if len(value) != DigestHexLen {
		return nil, false
	}
	if value != strings.ToLower(value) {
		return nil, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != DigestHexBytes {
		return nil, false
	}
	return decoded, true
}

func hasDuplicateStrings(input []string) bool {
	if len(input) <= 1 {
		return false
	}

	seen := make(map[string]struct{}, len(input))
	for _, value := range input {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}

	return false
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
