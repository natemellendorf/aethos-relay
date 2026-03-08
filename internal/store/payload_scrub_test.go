package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

func TestScrubInvalidPayloadMessages(t *testing.T) {
	f, err := os.CreateTemp("", "relay-scrub-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	st := NewBBoltStore(path)
	if err := st.Open(); err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC().Truncate(time.Second)
	messages := []*model.Message{
		{
			ID:        "valid-1",
			From:      "sender-a",
			To:        "recipient-a",
			Payload:   "QQ",
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		},
		{
			ID:        "invalid-padded",
			From:      "sender-b",
			To:        "recipient-a",
			Payload:   "QQ==",
			CreatedAt: now.Add(time.Second),
			ExpiresAt: now.Add(time.Hour),
		},
		{
			ID:        "invalid-whitespace",
			From:      "sender-c",
			To:        "recipient-b",
			Payload:   " QQ",
			CreatedAt: now.Add(2 * time.Second),
			ExpiresAt: now.Add(time.Hour),
		},
	}

	for _, m := range messages {
		if err := st.PersistMessage(context.Background(), m); err != nil {
			t.Fatalf("persist message %s: %v", m.ID, err)
		}
	}

	report, err := ScrubInvalidPayloadMessages(context.Background(), st)
	if err != nil {
		t.Fatalf("scrub returned error: %v", err)
	}
	if report.InvalidFound != 2 {
		t.Fatalf("expected 2 invalid messages, got %d", report.InvalidFound)
	}
	if report.Removed != 2 {
		t.Fatalf("expected 2 removed messages, got %d", report.Removed)
	}

	if _, err := st.GetMessageByID(context.Background(), "valid-1"); err != nil {
		t.Fatalf("expected valid message to remain: %v", err)
	}
	if _, err := st.GetMessageByID(context.Background(), "invalid-padded"); err == nil {
		t.Fatal("expected padded invalid message to be removed")
	}
	if _, err := st.GetMessageByID(context.Background(), "invalid-whitespace"); err == nil {
		t.Fatal("expected whitespace invalid message to be removed")
	}
}
