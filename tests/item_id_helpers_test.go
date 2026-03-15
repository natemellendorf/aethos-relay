package tests

import (
	"testing"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
)

func testItemID(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) string {
	return gossipv1.ComputeItemID(from, to, payloadB64, createdAt, expiresAt)
}

func mustTransferObject(t *testing.T, itemID string, from string, to string, payloadB64 string, createdAt int64, expiresAt int64) gossipv1.TransferObject {
	t.Helper()
	envelopeB64, err := gossipv1.EncodeItemEnvelopeB64(from, to, payloadB64, createdAt, expiresAt)
	if err != nil {
		t.Fatalf("encode item envelope: %v", err)
	}
	return gossipv1.TransferObject{
		ItemID:       itemID,
		EnvelopeB64:  envelopeB64,
		ExpiryUnixMS: uint64(expiresAt) * 1000,
		HopCount:     0,
	}
}
