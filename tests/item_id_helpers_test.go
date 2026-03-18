package tests

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
)

func testItemID(from string, to string, payloadB64 string, createdAt int64, expiresAt int64) string {
	_ = createdAt
	_ = expiresAt
	envelopeB64 := mustSignedEnvelopeB64(nil, from, to, payloadB64)
	return gossipv1.ComputeTransferObjectItemID(gossipv1.TransferObject{EnvelopeB64: envelopeB64})
}

func mustTransferObject(t *testing.T, itemID string, from string, to string, payloadB64 string, createdAt int64, expiresAt int64) gossipv1.TransferObject {
	t.Helper()
	_ = createdAt
	_ = expiresAt
	if _, err := base64.RawURLEncoding.Strict().DecodeString(payloadB64); err != nil {
		return gossipv1.TransferObject{
			ItemID:       itemID,
			EnvelopeB64:  "%%%",
			ExpiryUnixMS: uint64(expiresAt) * 1000,
			HopCount:     0,
		}
	}
	envelopeB64 := mustSignedEnvelopeB64(t, from, to, payloadB64)
	computedID := gossipv1.ComputeTransferObjectItemID(gossipv1.TransferObject{EnvelopeB64: envelopeB64})
	if itemID == "" {
		itemID = computedID
	}
	return gossipv1.TransferObject{
		ItemID:       itemID,
		EnvelopeB64:  envelopeB64,
		ExpiryUnixMS: uint64(expiresAt) * 1000,
		HopCount:     0,
	}
}

func mustSignedEnvelopeB64(t *testing.T, from string, to string, payloadB64 string) string {
	hexOrDigest := func(value string) string {
		if gossipv1.IsDigestHexID(value) {
			return value
		}
		digest := sha256.Sum256([]byte(value))
		return hex.EncodeToString(digest[:])
	}
	manifestID := hexOrDigest(from)
	toID := hexOrDigest(to)
	body, err := base64.RawURLEncoding.Strict().DecodeString(payloadB64)
	if err != nil {
		body = []byte(payloadB64)
		payloadB64 = base64.RawURLEncoding.EncodeToString(body)
	}
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		if t != nil {
			t.Fatalf("canonical mode: %v", err)
		}
		panic(err)
	}
	toBytes, _ := hex.DecodeString(toID)
	manifestBytes, _ := hex.DecodeString(manifestID)
	seed := sha256.Sum256([]byte("test-author:" + from))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signingPayload, err := mode.Marshal(map[string]any{"to_wayfarer_id": toBytes, "manifest_id": manifestBytes, "body": body})
	if err != nil {
		if t != nil {
			t.Fatalf("encode signing payload: %v", err)
		}
		panic(err)
	}
	digest := sha256.Sum256(append([]byte("AETHOS_ENVELOPE_V1"), signingPayload...))
	authorSig := ed25519.Sign(privateKey, digest[:])
	envelopeB64, err := gossipv1.EncodeTransferEnvelopeB64(toID, manifestID, payloadB64, publicKey, authorSig)
	if err != nil {
		if t != nil {
			t.Fatalf("encode transfer envelope: %v", err)
		}
		panic(err)
	}
	return envelopeB64
}
