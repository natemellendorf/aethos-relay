# Protocol Divergence Audit

Last audited: 2026-03-08

This document tracks implementation deltas versus canonical protocol specs in `aethos`.

## Client <-> Relay status after legacy cleanup

The following previously transitional behaviors are now canonical-only in relay runtime:

- `hello` requires both `wayfarer_id` and `device_id`.
- `hello_ok` includes `relay_id`.
- `send_ok` and `message` no longer emit legacy `at` alias.
- `messages[]` entries are canonical-only (`msg_id`, `from`, `payload_b64`, `received_at`).
- Client `payload_b64` validation is strict RFC4648 base64url raw (unpadded, no whitespace).
- Outbound client `payload_b64` is always canonical base64url raw.
- Error frames emit only canonical fields (`type`, `code`, `message`), with no `msg_id` alias.
- Client `ack` recipient binding is canonical connection identity `(wayfarer_id, device_id)`.
- Client suppression semantics are canonical ack-driven.

## Remaining notable divergences

- Client TTL default still follows relay `maxTTL` instead of canonical `3600`.
- Expired messages may still be visible prior to sweep.
- `client_msg_id` idempotency contract is not implemented.
- Federation protocol remains inventory/request/message-forward based (not full canonical envelope-forward model).
