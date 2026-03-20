# Compatibility test harness

This repository includes deterministic compatibility tests for the canonical Gossip V1 path.

## Integration tests

- `tests/compatibility_harness_test.go::TestCompatibilityHarnessCanonicalClientRelayPath`
  - validates HELLO/SUMMARY/REQUEST/TRANSFER/RECEIPT client flow
  - validates accepted vs rejected transfer object handling

- `tests/compatibility_harness_test.go::TestCompatibilityHarnessRejectsNonBinaryFrames`
  - validates non-binary websocket frame rejection

## Federation interoperability tests

Relay-to-relay conformance vectors are covered in:

- `tests/gossipv1_federation_relay_test.go`
- `docs/federation/CONFORMANCE_VECTORS.md`
