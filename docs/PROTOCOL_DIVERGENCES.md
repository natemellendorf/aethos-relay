# Protocol Divergence Audit

Last audited: 2026-03-08

This document tracks implementation deltas versus canonical protocol specs in `aethos`.

## Client <-> Relay runtime modes

Default runtime remains compatibility-first. Legacy behavior is still present unless strict mode is enabled.

### Default mode (compatibility, strict mode OFF)

- Existing compatibility paths remain available (legacy payload acceptance, legacy timestamp alias emission, legacy error `msg_id` alias, and ack fallback behavior).
- No legacy compatibility was removed in this bead because the cleanup plan currently has no greenlit removals.

### Strict mode (canonical-only)

Enable with `AETHOS_CLIENT_RELAY_STRICT_V1=1`.

When enabled, client WebSocket v1 behavior is canonical-only:

- Handshake enforces `hello` first; non-hello pre-handshake frames receive canonical `error {type,code,message}` and connection close.
- `hello` requires `device_id` and validates `wayfarer_id`/`send.to` as lowercase 64-hex.
- Client `payload_b64` must be RFC4648 base64url raw (unpadded, no whitespace).
- `send.to` must match decoded EnvelopeV1 recipient (`TO_MISMATCH` on mismatch).
- Error frames emit canonical `code`+`message` only (no legacy `msg_id` alias).
- `send_ok`, pushed `message`, and pull entries do not emit legacy `at` field.
- Strict TTL behavior: default `ttl_seconds=3600` when omitted; expired messages (`now_seconds >= expires_at`) are not deliverable.
- Ack is bound to authenticated `(wayfarer_id, device_id)` only.
- Pull default limit is 50 when omitted; strict pull entries use canonical shape.

## Remaining notable divergences

- In default compatibility mode, client TTL default follows relay `maxTTL` (strict mode uses canonical 3600).
- In default compatibility mode, expired messages may still be visible prior to sweep.
- `client_msg_id` idempotency contract is not implemented.
- Federation protocol remains inventory/request/message-forward based (not full canonical envelope-forward model).
