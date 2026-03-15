package tests

import "github.com/natemellendorf/aethos-relay/internal/gossipv1"

func testItemID(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) string {
	return gossipv1.ComputeItemID(from, to, payloadB64, createdAt, expiresAt)
}
