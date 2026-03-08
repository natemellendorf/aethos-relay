package store

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// PayloadScrubReport summarizes startup scrub activity.
type PayloadScrubReport struct {
	RecipientsScanned int
	QueuedScanned     int
	InvalidFound      int
	Removed           int
	LookupErrors      int
	RemoveErrors      int
}

// ScrubInvalidPayloadMessages removes queued messages whose payload_b64 fails canonical decode.
// This is intended for one-time cleanup of legacy padded payloads.
func ScrubInvalidPayloadMessages(ctx context.Context, st Store) (PayloadScrubReport, error) {
	var report PayloadScrubReport
	recipients, err := st.GetAllRecipientIDs(ctx)
	if err != nil {
		return report, fmt.Errorf("list recipients: %w", err)
	}
	report.RecipientsScanned = len(recipients)

	var firstErr error
	for _, recipientID := range recipients {
		msgIDs, err := st.GetAllQueuedMessageIDs(ctx, recipientID)
		if err != nil {
			report.LookupErrors++
			if firstErr == nil {
				firstErr = fmt.Errorf("list queued msg ids for recipient=%s: %w", recipientID, err)
			}
			continue
		}

		for _, msgID := range msgIDs {
			report.QueuedScanned++
			msg, err := st.GetMessageByID(ctx, msgID)
			if err != nil {
				report.LookupErrors++
				if firstErr == nil {
					firstErr = fmt.Errorf("load queued message msg_id=%s recipient=%s: %w", msgID, recipientID, err)
				}
				continue
			}

			if _, err := model.DecodePayloadB64(msg.Payload); err == nil {
				continue
			}

			report.InvalidFound++
			if err := st.RemoveMessage(ctx, msgID); err != nil {
				report.RemoveErrors++
				if firstErr == nil {
					firstErr = fmt.Errorf("remove invalid payload msg_id=%s recipient=%s: %w", msgID, recipientID, err)
				}
				continue
			}

			report.Removed++
			log.Printf("store: scrub removed invalid payload msg_id=%s recipient=%s from=%s to=%s shape={%s}", msg.ID, recipientID, msg.From, msg.To, payloadB64Shape(msg.Payload))
		}
	}

	return report, firstErr
}

func payloadB64Shape(raw string) string {
	hasWhitespace := false
	for _, r := range raw {
		if unicode.IsSpace(r) {
			hasWhitespace = true
			break
		}
	}

	return fmt.Sprintf("len=%d padding=%t plus=%t slash=%t dash=%t underscore=%t whitespace=%t",
		len(raw),
		strings.Contains(raw, "="),
		strings.Contains(raw, "+"),
		strings.Contains(raw, "/"),
		strings.Contains(raw, "-"),
		strings.Contains(raw, "_"),
		hasWhitespace,
	)
}
