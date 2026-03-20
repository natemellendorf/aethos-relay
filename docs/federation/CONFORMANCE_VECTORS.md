# Gossip V1 relay profile conformance vectors

These vectors summarize accepted and rejected forwarding behavior expected under Option B canonical profile.

## Accepted vectors

1. Valid HELLO/SUMMARY/REQUEST/TRANSFER/RECEIPT encounter persists transferred object.
2. Duplicate transfer object from same source relay is idempotent (no second durable write) and remains receipt-acknowledgeable.
3. Cross-relay propagation: object accepted at source relay, requested by peer relay, persisted on peer.

## Rejected vectors

1. TRANSFER object with invalid `envelope_b64` encoding rejected.
2. TRANSFER object with invalid `item_id` hash mismatch rejected.
3. TRANSFER object with expired `expiry_unix_ms` rejected.
4. TRANSFER object with invalid `hop_count > 65535` rejected.
5. Session-level rejection for non-binary transport frame.

## Loop / duplicate / expiry expectations

- Loop suppression uses seen-by-source behavior in ingest engine (`seen_loop`).
- Duplicate object ingest is idempotent.
- Expired objects are not persisted and are excluded from acceptance receipt IDs.
- RECEIPT acknowledgements use wire field `received[]` (no legacy `accepted`/`rejected` keys).

## Test coverage in this repo

- `tests/gossipv1_federation_relay_test.go`
- `tests/compatibility_harness_test.go`
- `internal/storeforward/engine_test.go`
- `internal/api/ws_test.go`
- `internal/gossipv1/protocol_test.go`
