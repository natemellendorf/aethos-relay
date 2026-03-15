package gossipv1

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseReceiptPayloadRejectsRejectedEntryMissingIDAndIndex(t *testing.T) {
	_, err := ParseReceiptPayload(map[string]any{
		"accepted": []string{},
		"rejected": []map[string]any{{
			"reason": "invalid transfer object",
		}},
	})
	if err == nil {
		t.Fatal("expected parse error for rejected entry missing id/index")
	}
	if !strings.Contains(err.Error(), "must include id or index") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseReceiptPayloadRejectsDuplicateAcceptedIDs(t *testing.T) {
	_, err := ParseReceiptPayload(map[string]any{
		"accepted": []string{"msg-1", "msg-1"},
	})
	if err == nil {
		t.Fatal("expected parse error for duplicate accepted ids")
	}
	if !strings.Contains(err.Error(), "contains duplicate ids") {
		t.Fatalf("unexpected error: %v", err)
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
		From:       from,
		To:         to,
		PayloadB64: payloadB64,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
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
