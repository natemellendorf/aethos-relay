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
	summary := BuildSummaryPayload([]string{"a", "b", "a"})

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
	if len(decoded.Payload) != 2 {
		t.Fatalf("summary payload must contain only canonical keys, got %#v", decoded.Payload)
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
}

func TestParseSummaryPayloadAcceptsLegacyHaveForCompatibility(t *testing.T) {
	parsed, err := ParseSummaryPayload(map[string]any{
		"have": []string{"legacy-1", "legacy-1", "legacy-2"},
	})
	if err != nil {
		t.Fatalf("parse legacy summary payload: %v", err)
	}
	if len(parsed.Have) != 2 || parsed.Have[0] != "legacy-1" || parsed.Have[1] != "legacy-2" {
		t.Fatalf("unexpected normalized legacy have: %#v", parsed.Have)
	}
	if parsed.ItemCount != 2 {
		t.Fatalf("unexpected legacy-derived item_count: got=%d want=%d", parsed.ItemCount, 2)
	}
	if len(parsed.BloomFilter) != BloomFilterBytes {
		t.Fatalf("unexpected legacy-derived bloom length: got=%d want=%d", len(parsed.BloomFilter), BloomFilterBytes)
	}
}

func TestBloomFilterMightContainMatchesInsertedItems(t *testing.T) {
	summary := BuildSummaryPayload([]string{"item-1", "item-2"})
	if !BloomFilterMightContain(summary.BloomFilter, "item-1") {
		t.Fatal("expected bloom filter to contain item-1")
	}
	if !BloomFilterMightContain(summary.BloomFilter, "item-2") {
		t.Fatal("expected bloom filter to contain item-2")
	}

	if BloomFilterMightContain(summary.BloomFilter[:BloomFilterBytes-1], "item-1") {
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
	left := BuildSummaryPayload([]string{"a", "b", "c"})
	right := BuildSummaryPayload([]string{"a", "b", "c"})

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
