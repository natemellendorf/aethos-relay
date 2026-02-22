# Compatibility test harness

This repository now includes a deterministic compatibility test harness for RelayLink v0.1.

## CLI client

`cmd/relaylink` provides a simple WebSocket CLI with commands:

- `hello --wayfarer-id <id>`
- `send --wayfarer-id <id> --to <wayfarer_id> --payload-file <path> --ttl <seconds>`
- `pull --wayfarer-id <id> --limit <N> [--out-dir <dir>]`
- `ack --wayfarer-id <id> --msg-id <uuid>`

Behavior:

- `send` reads raw bytes and base64-encodes before transmit.
- `pull` decodes base64 payloads (and can write decoded bytes to `--out-dir`).
- output is JSON for deterministic test harness usage.

## Deterministic fixture

See `tests/fixtures/README.md` for the deterministic CBOR fixture and exact generation command.

## Integration tests

- `tests/compatibility_harness_test.go::TestRelayLinkCompatibilityPayloadIntegrityAndAck` validates:
  - hello handshake
  - send
  - pull
  - byte-for-byte payload equality
  - ack behavior with empty queue verification
  - TTL preservation in persisted message metadata

- `tests/compatibility_harness_test.go::TestRelayLinkCompatibilityFederationOptional` is opt-in and runs when:

```bash
AETHOS_RELAY_TEST_FEDERATION=1 go test ./tests -run Federation
```

This test validates federated forwarding with unchanged payload bytes.
