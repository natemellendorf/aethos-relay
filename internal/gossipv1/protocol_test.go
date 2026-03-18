package gossipv1

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseReceiptPayloadRejectsUnknownRejectedKey(t *testing.T) {
	_, err := ParseReceiptPayload(map[string]any{
		"received": []string{},
		"rejected": []string{"legacy"},
	})
	if err == nil {
		t.Fatal("expected parse error for unknown receipt payload key")
	}
	if !strings.Contains(err.Error(), "unknown receipt payload field \"rejected\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseReceiptPayloadRejectsDuplicateAcceptedIDs(t *testing.T) {
	_, err := ParseReceiptPayload(map[string]any{
		"received": []string{"msg-1", "msg-1"},
	})
	if err == nil {
		t.Fatal("expected parse error for duplicate accepted ids")
	}
	if !strings.Contains(err.Error(), "contains duplicate ids") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncodeReceiptPayloadEmitsReceivedWithoutAccepted(t *testing.T) {
	receipt := ReceiptPayload{Accepted: []string{"msg-1"}}

	frame, err := EncodeEnvelope(FrameTypeReceipt, receipt)
	if err != nil {
		t.Fatalf("encode receipt envelope: %v", err)
	}

	decoded, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatalf("decode receipt envelope: %v", err)
	}

	if _, ok := decoded.Payload["accepted"]; ok {
		t.Fatalf("receipt payload must not include accepted key: %#v", decoded.Payload)
	}
	received, ok := decoded.Payload["received"].([]any)
	if !ok {
		t.Fatalf("receipt payload must include received array: %#v", decoded.Payload)
	}
	if len(received) != 1 || received[0] != "msg-1" {
		t.Fatalf("unexpected received ids: %#v", received)
	}
}

func TestParseReceiptPayloadRejectsLegacyAcceptedKey(t *testing.T) {
	_, err := ParseReceiptPayload(map[string]any{
		"accepted": []string{"msg-1"},
	})
	if err == nil {
		t.Fatal("expected parse error for legacy accepted key")
	}
	if !strings.Contains(err.Error(), "unknown receipt payload field \"accepted\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseTransferPayloadMixedAcceptsValidObject(t *testing.T) {
	envelopeB64 := mustSignedTransferEnvelopeB64(t, strings.Repeat("aa", 32), strings.Repeat("bb", 32), "QQ")
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 0 {
		t.Fatalf("expected no rejected objects, got %#v", parsed.Rejected)
	}
	if len(parsed.Objects) != 1 || parsed.Objects[0].Object.ItemID != itemID {
		t.Fatalf("unexpected parsed objects: %#v", parsed.Objects)
	}
}

func TestParseTransferPayloadMixedRejectsMissingItemID(t *testing.T) {
	envelopeB64 := mustSignedTransferEnvelopeB64(t, strings.Repeat("aa", 32), strings.Repeat("bb", 32), "QQ")

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "missing_item_id" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
}

func TestParseTransferPayloadMixedRejectsMissingEnvelopeB64(t *testing.T) {
	itemID := strings.Repeat("11", 32)
	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "missing_envelope_b64" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
}

func TestParseTransferPayloadMixedRejectsInvalidEnvelopeEncoding(t *testing.T) {
	itemID := strings.Repeat("22", 32)
	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   "%%%",
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "invalid_envelope_encoding" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
	if parsed.Rejected[0].Diagnostic == nil {
		t.Fatal("expected rejection diagnostics")
	}
	if parsed.Rejected[0].Diagnostic.Base64URLDecodeOK {
		t.Fatalf("expected base64url decode failure diagnostics, got %#v", parsed.Rejected[0].Diagnostic)
	}
}

func TestParseTransferPayloadMixedRejectsInvalidExpiryUnixMS(t *testing.T) {
	envelopeB64 := mustSignedTransferEnvelopeB64(t, strings.Repeat("aa", 32), strings.Repeat("bb", 32), "QQ")
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": "bad",
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "invalid_expiry_unix_ms" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
}

func TestParseTransferPayloadMixedRejectsInvalidHopCount(t *testing.T) {
	envelopeB64 := mustSignedTransferEnvelopeB64(t, strings.Repeat("aa", 32), strings.Repeat("bb", 32), "QQ")
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(65536),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "invalid_hop_count" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
}

func TestParseTransferPayloadMixedMixedBatchKeepsValidObjects(t *testing.T) {
	envelopeB64 := mustSignedTransferEnvelopeB64(t, strings.Repeat("aa", 32), strings.Repeat("bb", 32), "QQ")
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}, {
			"item_id":        strings.Repeat("ab", 32),
			"envelope_b64":   "%%%",
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Objects) != 1 || parsed.Objects[0].Object.ItemID != itemID {
		t.Fatalf("unexpected accepted objects: %#v", parsed.Objects)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "invalid_envelope_encoding" {
		t.Fatalf("unexpected rejection: %#v", parsed.Rejected)
	}
}

func TestParseTransferPayloadMixedAcceptsCanonicalTransferEnvelopeSchema(t *testing.T) {
	toWayfarerID := strings.Repeat("aa", 32)
	manifestID := strings.Repeat("bb", 32)
	envelopeB64 := mustSignedTransferEnvelopeB64(t, toWayfarerID, manifestID, "QQ")
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 0 {
		t.Fatalf("expected no rejected objects, got %#v", parsed.Rejected)
	}
	if len(parsed.Objects) != 1 {
		t.Fatalf("expected one accepted object, got %#v", parsed.Objects)
	}
	if parsed.Objects[0].Object.Envelope.To != toWayfarerID {
		t.Fatalf("unexpected envelope destination: %q", parsed.Objects[0].Object.Envelope.To)
	}
}

func TestParseTransferPayloadMixedRejectsUnknownEnvelopeKeys(t *testing.T) {
	toBytes := bytes.Repeat([]byte{0xaa}, DigestHexBytes)
	manifestBytes := bytes.Repeat([]byte{0xbb}, DigestHexBytes)
	body := []byte("hello")
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signingPayload, err := canonicalEncMode.Marshal(canonicalTransferSigningPayload{
		ToWayfarerID: toBytes,
		ManifestID:   manifestBytes,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("encode signing payload: %v", err)
	}
	digest := sha256.Sum256(append([]byte(envelopeSignatureDomain), signingPayload...))
	authorSig := ed25519.Sign(privateKey, digest[:])

	envelopeMap := map[string]any{
		"to_wayfarer_id": toBytes,
		"manifest_id":    manifestBytes,
		"body":           body,
		"author_pubkey":  []byte(publicKey),
		"author_sig":     authorSig,
		"x_ext":          []byte("ignored"),
	}
	envelopeBytes, err := canonicalEncMode.Marshal(envelopeMap)
	if err != nil {
		t.Fatalf("encode envelope map: %v", err)
	}
	envelopeB64 := base64.RawURLEncoding.EncodeToString(envelopeBytes)
	itemID := ComputeTransferObjectItemID(TransferObject{EnvelopeB64: envelopeB64})

	parsed, err := ParseTransferPayloadMixed(map[string]any{
		"objects": []map[string]any{{
			"item_id":        itemID,
			"envelope_b64":   envelopeB64,
			"expiry_unix_ms": uint64(1710003600000),
			"hop_count":      uint64(0),
		}},
	})
	if err != nil {
		t.Fatalf("parse transfer payload: %v", err)
	}
	if len(parsed.Rejected) != 1 || parsed.Rejected[0].Reason != "policy_reject" {
		t.Fatalf("expected unknown envelope key rejection, got %#v", parsed.Rejected)
	}
}

func TestEncodeReceiptPayloadContainsOnlyReceivedKey(t *testing.T) {
	frame, err := EncodeEnvelope(FrameTypeReceipt, ReceiptPayload{Accepted: []string{strings.Repeat("33", 32)}})
	if err != nil {
		t.Fatalf("encode receipt envelope: %v", err)
	}
	decoded, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatalf("decode receipt envelope: %v", err)
	}
	if len(decoded.Payload) != 1 {
		t.Fatalf("receipt payload must include exactly one key, got %#v", decoded.Payload)
	}
	if _, ok := decoded.Payload["received"]; !ok {
		t.Fatalf("receipt payload must include received key, got %#v", decoded.Payload)
	}
}

func TestBuildSummaryPayloadEncodesCanonicalFieldsOnly(t *testing.T) {
	idA := strings.Repeat("11", 32)
	idB := strings.Repeat("22", 32)
	summary := BuildSummaryPayload([]string{idB, idA, idA})

	frame, err := EncodeEnvelope(FrameTypeSummary, summary)
	if err != nil {
		t.Fatalf("encode summary envelope: %v", err)
	}

	decoded, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatalf("decode summary envelope: %v", err)
	}
	if decoded.Type != FrameTypeSummary {
		t.Fatalf("unexpected frame type: %s", decoded.Type)
	}
	if len(decoded.Payload) != 4 {
		t.Fatalf("summary payload must contain only canonical keys, got %#v", decoded.Payload)
	}
	if _, ok := decoded.Payload["preview_item_ids"]; !ok {
		t.Fatalf("summary payload must include preview_item_ids key: %#v", decoded.Payload)
	}
	if _, ok := decoded.Payload["preview_cursor"]; !ok {
		t.Fatalf("summary payload must include preview_cursor key: %#v", decoded.Payload)
	}
	if _, ok := decoded.Payload["have"]; ok {
		t.Fatalf("summary payload must not include legacy have key: %#v", decoded.Payload)
	}

	parsed, err := ParseSummaryPayload(decoded.Payload)
	if err != nil {
		t.Fatalf("parse canonical summary payload: %v", err)
	}
	if len(parsed.BloomFilter) != BloomFilterBytes {
		t.Fatalf("unexpected bloom filter length: got=%d want=%d", len(parsed.BloomFilter), BloomFilterBytes)
	}
	if parsed.ItemCount != 2 {
		t.Fatalf("unexpected item_count: got=%d want=%d", parsed.ItemCount, 2)
	}
	if len(parsed.PreviewItemIDs) != 2 || parsed.PreviewItemIDs[0] != idA || parsed.PreviewItemIDs[1] != idB {
		t.Fatalf("unexpected preview_item_ids: %#v", parsed.PreviewItemIDs)
	}
	if parsed.PreviewCursor != idB {
		t.Fatalf("unexpected preview_cursor: got=%q want=%q", parsed.PreviewCursor, idB)
	}
}

func TestBloomFilterMightContainMatchesInsertedItems(t *testing.T) {
	idA := strings.Repeat("66", 32)
	idB := strings.Repeat("77", 32)
	summary := BuildSummaryPayload([]string{idA, idB})
	if !BloomFilterMightContain(summary.BloomFilter, idA) {
		t.Fatal("expected bloom filter to contain idA")
	}
	if !BloomFilterMightContain(summary.BloomFilter, idB) {
		t.Fatal("expected bloom filter to contain idB")
	}

	if BloomFilterMightContain(summary.BloomFilter[:BloomFilterBytes-1], idA) {
		t.Fatal("invalid bloom filter size must fail closed")
	}
}

func TestParseSummaryPayloadRejectsNonBytesBloomFilter(t *testing.T) {
	_, err := ParseSummaryPayload(map[string]any{
		"bloom_filter": []any{uint64(1), uint64(2)},
		"item_count":   uint64(2),
	})
	if err == nil {
		t.Fatal("expected parse error for non-bytes bloom_filter")
	}
	if !strings.Contains(err.Error(), "bloom_filter must be bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSummaryPayloadRejectsWrongBloomFilterLength(t *testing.T) {
	_, err := ParseSummaryPayload(map[string]any{
		"bloom_filter": make([]byte, BloomFilterBytes-1),
		"item_count":   uint64(1),
	})
	if err == nil {
		t.Fatal("expected parse error for wrong bloom_filter length")
	}
	if !strings.Contains(err.Error(), "must be 2048 bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildSummaryPayloadDeterministic(t *testing.T) {
	idA := strings.Repeat("88", 32)
	idB := strings.Repeat("99", 32)
	idC := strings.Repeat("aa", 32)
	left := BuildSummaryPayload([]string{idA, idB, idC})
	right := BuildSummaryPayload([]string{idA, idB, idC})

	if left.ItemCount != right.ItemCount {
		t.Fatalf("determinism item_count mismatch: left=%d right=%d", left.ItemCount, right.ItemCount)
	}
	if len(left.BloomFilter) != len(right.BloomFilter) {
		t.Fatalf("determinism bloom length mismatch: left=%d right=%d", len(left.BloomFilter), len(right.BloomFilter))
	}
	for i := range left.BloomFilter {
		if left.BloomFilter[i] != right.BloomFilter[i] {
			t.Fatalf("determinism bloom mismatch at index %d", i)
		}
	}

	// Guard against accidental zeroed output for non-empty sets.
	var nonZero bool
	for _, b := range left.BloomFilter {
		if b != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("expected non-zero bloom filter for non-empty input")
	}
}

func TestBuildSummaryPayloadDeterminismForCanonicalHexID(t *testing.T) {
	itemID := strings.Repeat("ab", 32)
	summary := BuildSummaryPayload([]string{itemID})
	if len(summary.BloomFilter) != BloomFilterBytes {
		t.Fatalf("unexpected bloom length: %d", len(summary.BloomFilter))
	}

	// Re-run independently and compare a stable probe point.
	again := BuildSummaryPayload([]string{itemID})
	if binary.BigEndian.Uint64(summary.BloomFilter[:8]) != binary.BigEndian.Uint64(again.BloomFilter[:8]) {
		t.Fatal("expected deterministic bloom output for canonical hex item IDs")
	}
}

func TestParseSummaryPayloadIgnoresUnknownKeys(t *testing.T) {
	idA := strings.Repeat("bc", 32)
	bloom := BuildSummaryPayload([]string{idA}).BloomFilter

	parsed, err := ParseSummaryPayload(map[string]any{
		"bloom_filter":     bloom,
		"item_count":       uint64(1),
		"preview_item_ids": []string{idA},
		"preview_cursor":   idA,
		"unexpected":       uint64(123),
	})
	if err != nil {
		t.Fatalf("parse summary with unknown key: %v", err)
	}
	if len(parsed.PreviewItemIDs) != 1 || parsed.PreviewItemIDs[0] != idA {
		t.Fatalf("unexpected preview ids: %#v", parsed.PreviewItemIDs)
	}
}

func TestParseSummaryPayloadRejectsUnsortedPreviewItemIDs(t *testing.T) {
	idA := strings.Repeat("01", 32)
	idB := strings.Repeat("02", 32)
	bloom := BuildSummaryPayload([]string{idA, idB}).BloomFilter

	_, err := ParseSummaryPayload(map[string]any{
		"bloom_filter":     bloom,
		"item_count":       uint64(2),
		"preview_item_ids": []string{idB, idA},
		"preview_cursor":   idA,
	})
	if err == nil {
		t.Fatal("expected parse error for unsorted preview_item_ids")
	}
}

func TestParseSummaryPayloadRejectsDuplicatePreviewItemIDs(t *testing.T) {
	idA := strings.Repeat("0a", 32)
	bloom := BuildSummaryPayload([]string{idA}).BloomFilter

	_, err := ParseSummaryPayload(map[string]any{
		"bloom_filter":     bloom,
		"item_count":       uint64(1),
		"preview_item_ids": []string{idA, idA},
		"preview_cursor":   idA,
	})
	if err == nil {
		t.Fatal("expected parse error for duplicate preview_item_ids")
	}
}

func TestComputeItemIDMatchesTransferObjectComputation(t *testing.T) {
	from := "sender-a"
	to := "recipient-a"
	payloadB64 := "QQ"
	createdAt := int64(1710000000)
	expiresAt := int64(1710003600)

	id := ComputeItemID(from, to, payloadB64, createdAt, expiresAt)
	if !IsDigestHexID(id) {
		t.Fatalf("expected digest item id, got %q", id)
	}

	transferID := ComputeTransferObjectItemID(TransferObject{
		EnvelopeB64: base64.RawURLEncoding.EncodeToString(mustCanonicalItemEnvelopeBytes(from, to, payloadB64, createdAt, expiresAt)),
	})
	if id != transferID {
		t.Fatalf("expected matching item ids, got direct=%q transfer=%q", id, transferID)
	}
}

func TestParseSummaryPayloadRejectsPreviewCursorWhenPreviewEmpty(t *testing.T) {
	bloom := BuildSummaryPayload(nil).BloomFilter
	_, err := ParseSummaryPayload(map[string]any{
		"bloom_filter":   bloom,
		"item_count":     uint64(0),
		"preview_cursor": strings.Repeat("cd", 32),
	})
	if err == nil {
		t.Fatal("expected parse error when preview_cursor present with empty preview")
	}
}

func TestBuildSummaryPayloadMatchesSpecBloomDeterminismVector(t *testing.T) {
	itemIDs := []string{
		strings.Repeat("00", 32),
		strings.Repeat("11", 32),
		strings.Repeat("22", 32),
	}

	summary := BuildSummaryPayload(itemIDs)
	if len(summary.BloomFilter) != BloomFilterBytes {
		t.Fatalf("unexpected bloom length: got=%d want=%d", len(summary.BloomFilter), BloomFilterBytes)
	}

	if summary.BloomFilter[129] != 0x10 {
		t.Fatalf("unexpected bloom byte[129]: got=0x%02x want=0x10", summary.BloomFilter[129])
	}
	if summary.BloomFilter[1317] != 0x02 {
		t.Fatalf("unexpected bloom byte[1317]: got=0x%02x want=0x02", summary.BloomFilter[1317])
	}
	if summary.BloomFilter[1953] != 0x80 {
		t.Fatalf("unexpected bloom byte[1953]: got=0x%02x want=0x80", summary.BloomFilter[1953])
	}

	expectedPrefix, err := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("decode expected prefix: %v", err)
	}
	if !bytes.Equal(summary.BloomFilter[:len(expectedPrefix)], expectedPrefix) {
		t.Fatalf("unexpected bloom prefix: got=%x want=%x", summary.BloomFilter[:len(expectedPrefix)], expectedPrefix)
	}
}

func TestParseRequestPayloadRejectsUnsortedWant(t *testing.T) {
	idA := strings.Repeat("01", 32)
	idB := strings.Repeat("02", 32)

	_, err := ParseRequestPayload(map[string]any{
		"want": []string{idB, idA},
	})
	if err == nil {
		t.Fatal("expected parse error for unsorted want")
	}
	if !strings.Contains(err.Error(), "want must be sorted by digest bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRequestPayloadRejectsDuplicateWant(t *testing.T) {
	idA := strings.Repeat("0a", 32)

	_, err := ParseRequestPayload(map[string]any{
		"want": []string{idA, idA},
	})
	if err == nil {
		t.Fatal("expected parse error for duplicate want entries")
	}
	if !strings.Contains(err.Error(), "want entries must be unique") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRequestPayloadRejectsInvalidWantItemID(t *testing.T) {
	_, err := ParseRequestPayload(map[string]any{
		"want": []string{"not-a-digest"},
	})
	if err == nil {
		t.Fatal("expected parse error for invalid want item id")
	}
	if !strings.Contains(err.Error(), "item id must be 64 lowercase hex chars") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustEnvelopeB64(t *testing.T, from string, to string, payloadB64 string, createdAt int64, expiresAt int64) string {
	t.Helper()
	envelopeB64, err := EncodeItemEnvelopeB64(from, to, payloadB64, createdAt, expiresAt)
	if err != nil {
		t.Fatalf("encode envelope b64: %v", err)
	}
	return envelopeB64
}

func mustSignedTransferEnvelopeB64(t *testing.T, toWayfarerID string, manifestID string, bodyB64 string) string {
	t.Helper()
	toBytes, err := hex.DecodeString(toWayfarerID)
	if err != nil {
		t.Fatalf("decode to_wayfarer_id: %v", err)
	}
	manifestBytes, err := hex.DecodeString(manifestID)
	if err != nil {
		t.Fatalf("decode manifest_id: %v", err)
	}
	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	seed := bytes.Repeat([]byte{0x24}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	signingPayload, err := canonicalEncMode.Marshal(canonicalTransferSigningPayload{
		ToWayfarerID: toBytes,
		ManifestID:   manifestBytes,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("encode signing payload: %v", err)
	}
	digest := sha256.Sum256(append([]byte(envelopeSignatureDomain), signingPayload...))
	authorSig := ed25519.Sign(privateKey, digest[:])

	envelopeB64, err := EncodeTransferEnvelopeB64(toWayfarerID, manifestID, bodyB64, publicKey, authorSig)
	if err != nil {
		t.Fatalf("encode transfer envelope: %v", err)
	}
	return envelopeB64
}
