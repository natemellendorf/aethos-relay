package gossipv1

import (
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
