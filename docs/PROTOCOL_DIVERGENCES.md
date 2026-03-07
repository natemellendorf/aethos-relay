# Protocol Divergence Audit

Last audited: 2026-03-07

This document audits current `aethos-relay` runtime behavior against canonical specs in local `aethos` worktrees.

## Canonical spec sources audited

- `/Users/natemellendorf/opencode/wt-aethos-aethos-dmm/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md`
- `/Users/natemellendorf/opencode/wt-aethos-aethos-dmm/docs/spec/FEDERATION_PROTOCOL_V1.md`
- `/Users/natemellendorf/opencode/wt-aethos-aethos-dmm/docs/spec/RECEIPTS.md`
- `/Users/natemellendorf/opencode/wt-aethos-aethos-dmm/docs/migration/protocol_update.md` (draft migration plan; planning input only, not protocol contract)
- `/Users/natemellendorf/opencode/aethos/docs/protocol.md` (canonical structures and canonical bytes)

## Client <-> Relay audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| `hello` / `device_id` | `hello` requires both `wayfarer_id` and `device_id` (`CLIENT_RELAY_PROTOCOL_V1.md:42`, `CLIENT_RELAY_PROTOCOL_V1.md:52`). | Wire frame now accepts optional `device_id` (`internal/model/message.go`), server still requires `wayfarer_id` and computes delivery identity from `(wayfarer_id, device_id)` when present (`internal/api/ws.go`); legacy clients without `device_id` fall back to wayfarer-only delivery identity for backward compatibility. | **Partially aligned (compat mode)** |
| `hello_ok` fields | `hello_ok` requires `relay_id` (`CLIENT_RELAY_PROTOCOL_V1.md:114`, `CLIENT_RELAY_PROTOCOL_V1.md:122`). | Relay returns only `{ "type": "hello_ok" }` (`internal/api/ws.go:230`). | **Diverges** |
| Payload encoding (`payload_b64`) | Must be base64url (no padding) and decode to canonical `EnvelopeV1` bytes (`CLIENT_RELAY_PROTOCOL_V1.md:15`, `CLIENT_RELAY_PROTOCOL_V1.md:16`, `CLIENT_RELAY_PROTOCOL_V1.md:70`; canonical bytes in `protocol.md:16`). | Server checks only non-empty payload (`internal/api/ws.go:248`), then stores payload string as-is (`internal/storeforward/client.go:52`). Compatibility tests use standard base64 (not base64url) encode/decode (`tests/compatibility_harness_test.go:27`, `tests/compatibility_harness_test.go:71`). | **Diverges** |
| `send_ok` timestamp names/shape | `send_ok` uses `msg_id` and optional `received_at`/`expires_at` pair (`CLIENT_RELAY_PROTOCOL_V1.md:128`, `CLIENT_RELAY_PROTOCOL_V1.md:142`, `CLIENT_RELAY_PROTOCOL_V1.md:144`). | Relay now emits `send_ok` with `msg_id`, canonical `received_at`/`expires_at` (Unix seconds), and retains legacy `at` as an alias of `received_at` during transition (`internal/api/ws.go`, `internal/model/message.go`). | **Partially aligned (compat alias retained)** |
| `message` timestamp names/shape | `message` requires `received_at` epoch seconds (`CLIENT_RELAY_PROTOCOL_V1.md:150`, `CLIENT_RELAY_PROTOCOL_V1.md:164`). | Push delivery now emits canonical `received_at` (Unix seconds) and retains legacy `at` alias for compatibility (`internal/api/ws.go`, `internal/federation/peering.go`, `internal/model/message.go`). | **Partially aligned (compat alias retained)** |
| `messages` object fields | Each pulled message should expose `msg_id`, `from`, `payload_b64`, `received_at` (`CLIENT_RELAY_PROTOCOL_V1.md:170`, `CLIENT_RELAY_PROTOCOL_V1.md:185`). | Pull response remains backward-compatible by marshaling legacy `model.Message` fields (including RFC3339 `at`, `expires_at`, `delivered`, etc.) and now adds canonical `received_at` (Unix seconds) alongside them during migration (`internal/api/ws.go`, `internal/model/message.go`). | **Partially aligned (migration dual-surface)** |
| `ack` durability | Ack state must be durable before treated complete (`CLIENT_RELAY_PROTOCOL_V1.md:289`). | `ack` writes delivery state in bbolt (`internal/api/ws.go:299`, `internal/storeforward/client.go:95`, `internal/store/bbolt_store.go:185`). | **Matches** |
| `ack` per-device binding | Ack binding must be `(wayfarer_id, device_id)`; one device ack must not suppress another (`CLIENT_RELAY_PROTOCOL_V1.md:275`, `CLIENT_RELAY_PROTOCOL_V1.md:281`). | Runtime now binds pull and tracked ack paths to connection delivery identity `(wayfarer_id, device_id)` when provided, with legacy wayfarer fallback when `device_id` is omitted or ack cannot be tied to a tracked delivery (`internal/api/ws.go`, `internal/storeforward/client.go:16`). Queue indexing remains wayfarer-scoped, so device identity is enforced via delivery-state filtering rather than separate per-device queues (`internal/storeforward/client.go`, `internal/store/bbolt_store.go`). | **Partially aligned** |
| TTL default (`ttl_seconds`) | Omitted TTL defaults to `3600` (`CLIENT_RELAY_PROTOCOL_V1.md:29`, `CLIENT_RELAY_PROTOCOL_V1.md:296`). | Omitted/non-positive TTL becomes `maxTTL` (`internal/storeforward/client.go:43`), and process default `maxTTL` is `604800` seconds (`cmd/relay/main.go:40`). | **Diverges** |
| `expires_at` immutability for a stored `msg_id` | `expires_at` must be immutable once accepted (`CLIENT_RELAY_PROTOCOL_V1.md:300`). | Message is persisted with fixed `ExpiresAt` (`internal/storeforward/client.go:54`, `internal/store/bbolt_store.go:108`), and ack path does not mutate it (`internal/store/bbolt_store.go:189`). | **Matches** |
| Expired message delivery | Expired messages must not be delivered (`CLIENT_RELAY_PROTOCOL_V1.md:301`). | Pull path explicitly documents expired messages may still be returned before sweep (`internal/storeforward/client.go:67`), and tests pin this behavior (`internal/storeforward/engine_test.go:310`, `internal/storeforward/engine_test.go:315`). Push path does not block expired messages prior to send (`internal/api/ws.go:366`). | **Diverges** |
| Error frame schema | `error` requires `code` + `message` (`CLIENT_RELAY_PROTOCOL_V1.md:207`, `CLIENT_RELAY_PROTOCOL_V1.md:216`, `CLIENT_RELAY_PROTOCOL_V1.md:225`). | `sendError` populates `type=error` and puts text into `msg_id` (`internal/api/ws.go:409`, `internal/api/ws.go:410`), with no `code` or `message` fields in `WSFrame` (`internal/model/message.go:42`). | **Diverges** |
| Idempotency via `client_msg_id` | Relay must dedupe by `(sender_wayfarer_id, client_msg_id)` when present (`CLIENT_RELAY_PROTOCOL_V1.md:260`, `CLIENT_RELAY_PROTOCOL_V1.md:266`). | `WSFrame` has no `client_msg_id` field (`internal/model/message.go:35`), and no idempotency logic or `IDEMPOTENCY_MISMATCH` handling exists in relay code (repo-wide search). | **Diverges** |

## Federation audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| Protocol model (envelope vs inventory/request/message) | Canonical federation is envelope-forwarding (`relay_hello`, `relay_forward(envelope)`, `relay_ack`, `relay_cover`) (`FEDERATION_PROTOCOL_V1.md:41`, `FEDERATION_PROTOCOL_V1.md:55`, `FEDERATION_PROTOCOL_V1.md:71`). | Runtime primarily uses `relay_inventory` + `relay_request` + `relay_forward(message)` flow (`internal/model/message.go:63`, `internal/model/message.go:64`, `internal/model/message.go:94`, `internal/federation/peering.go:343`, `internal/federation/peering.go:502`, `internal/federation/peering.go:533`). | **Diverges** |
| `relay_hello` shape | Requires `protocol_version` integer `1` (`FEDERATION_PROTOCOL_V1.md:49`). | Frame uses `version` string (`internal/model/message.go:75`), set to `"1.0"` (`internal/federation/peering.go:22`, `internal/federation/peering.go:233`), and inbound validation checks only `type` and non-empty `relay_id` (`internal/federation/peering.go:827`). | **Diverges** |
| `relay_forward` shape | Requires `envelope` with `envelope_id`, `destination`, `payload`, `created_at`, `expires_at`, `hop_count`, `seen_relays` (`FEDERATION_PROTOCOL_V1.md:62`, `FEDERATION_PROTOCOL_V1.md:69`). | Runtime still carries legacy `message`, but now also emits optional canonical timestamp metadata under `envelope.created_at`/`envelope.expires_at` (Unix ms), and validates envelope timestamp ordering/expiry when present on receive (`internal/model/message.go`, `internal/federation/peering.go`). | **Partially aligned (dual-surface compat mode)** |
| `relay_ack` shape and emission | `relay_ack` statuses are `accepted` or `rejected`; rejections carry `code`/`message` (`FEDERATION_PROTOCOL_V1.md:79`, `FEDERATION_PROTOCOL_V1.md:83`, `FEDERATION_PROTOCOL_V1.md:126`). | `RelayAckFrame` includes `destination` and free-form `status` (`internal/model/message.go:101`, `internal/model/message.go:102`); handler recognizes `accepted|duplicate|expired` (`internal/federation/peering.go:625`) and there is no `relay_ack` send path in `handleRelayForward` (`internal/federation/peering.go:553`). | **Diverges** |
| Hop count increment/enforcement (runtime path) | Reject if incoming `hop_count >= MAX_HOPS`; increment by exactly 1 before forwarding (`FEDERATION_PROTOCOL_V1.md:103`, `FEDERATION_PROTOCOL_V1.md:104`). | Runtime forward path sends raw `message` without `hop_count` (`internal/federation/peering.go:896`); hop helper exists (`internal/storeforward/federation_forwarding.go:87`) but is only exercised by tests, not by `PeerManager` forwarding path. | **Diverges** |
| `seen_relays` loop prevention | `seen_relays` must include local relay before forward; reject if local already present (`FEDERATION_PROTOCOL_V1.md:105`, `FEDERATION_PROTOCOL_V1.md:106`). | No `seen_relays` wire field in `RelayForwardFrame` (`internal/model/message.go:94`). Loop checks use envelope-store seen index keyed by envelope/message ID + relay ID (`internal/storeforward/federation_forwarding.go:50`, `internal/storeforward/federation_forwarding.go:73`, `internal/storeforward/federation_forwarding.go:127`). | **Diverges** |
| Destination mismatch validation | Must reject when `destination != hex_lower(EnvelopeV1.toWayfarerId)` (`FEDERATION_PROTOCOL_V1.md:109`, `FEDERATION_PROTOCOL_V1.md:133`; structure in `protocol.md:44`). | No canonical `destination` field is present in wire `relay_forward`; forward validation checks only message field presence (`internal/storeforward/federation_forwarding.go:203`) and does not decode canonical envelope payload for destination comparison. | **Diverges** |
| TTL non-extension | `expires_at` is immutable and must not be extended (`FEDERATION_PROTOCOL_V1.md:107`). | Forward acceptance persists incoming `msg.ExpiresAt` as-is (`internal/storeforward/federation_forwarding.go:63`, `internal/storeforward/federation_forwarding.go:199`), and tests pin expiry preservation (`internal/storeforward/engine_test.go:224`). | **Matches** |
| Expiry rejection boundary | Expired envelope threshold is `now_ms >= expires_at` (`FEDERATION_PROTOCOL_V1.md:108`). | Runtime expiry check is `now.After(expires_at)` (`internal/storeforward/federation_forwarding.go:45`), which differs at exact-equality boundary. | **Diverges** |
| `relay_cover` usage and shape | Requires `relay_id` and `sent_at` (Unix ms), optional padding (`FEDERATION_PROTOCOL_V1.md:93`, `FEDERATION_PROTOCOL_V1.md:94`, `FEDERATION_PROTOCOL_V1.md:98`). | Cover frame now dual-emits legacy `ts` (seconds) and canonical `sent_at` (ms), plus existing `nonce`; receive path still treats cover as no-op heartbeat (`internal/model/message.go`, `internal/federation/tar.go`, `internal/federation/peering.go:369`). | **Partially aligned (dual-surface compat mode)** |

## Receipts audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| Receipt transport wrapper support | Receipt wrappers use `receipt_scope` + `receipt_v1_b64` (base64url) (`RECEIPTS.md:58`, `RECEIPTS.md:66`), with inner `ReceiptV1` from canonical structures (`RECEIPTS.md:7`, `protocol.md:57`). | Relay does not expose or consume receipt wrapper fields; no receipt frame types are implemented in client or federation WebSocket handlers (`internal/api/ws.go:202`, `internal/federation/peering.go:336`). | **Diverges** |
| Non-conflation of device vs federation semantics | `DeviceReceipt` and `FederationReceipt` must not be conflated (`RECEIPTS.md:42`, `RECEIPTS.md:44`). | Client `ack` updates client-delivery state (`internal/api/ws.go:299`), while `relay_ack` updates peer metrics and tests assert it does not mark client delivery (`internal/federation/peering.go:616`, `internal/federation/peering_test.go:375`, `internal/federation/peering_test.go:380`). | **Matches** |
| `ack_ok` vs receipt semantics | Receipt semantics are `ReceiptV1`-based and scoped (`RECEIPTS.md:13`, `RECEIPTS.md:51`). | `ack_ok` exists as transport response frame to `ack` (`internal/api/ws.go:309`) but relay does not generate `ReceiptV1` wrappers; `ack_ok` should not be treated as canonical receipt object. | **Diverges** |

## Implementation Notes (non-protocol constraints)

These are implementation/runtime limits and policies; they are not canonical protocol contracts.

| Item | Current behavior (evidence) | Status |
| --- | --- | --- |
| WebSocket buffer sizes | Client upgrader uses `ReadBufferSize=1024`, `WriteBufferSize=1024` (`internal/api/ws.go:116`, `internal/api/ws.go:117`); federation upgrader uses same (`internal/federation/peering.go:795`, `internal/federation/peering.go:796`). | **VERIFY** |
| Backpressure buffers | Client send queue is `256` (`internal/api/ws.go:132`) with 1s enqueue timeout (`internal/api/ws.go:400`); federation peer send queue is `256` (`internal/federation/peering.go:223`, `internal/federation/peering.go:812`); TAR batch queue is `256` (`internal/federation/peering.go:415`). | **VERIFY** |
| Origin policy | Dev mode allows all origins (`internal/api/ws.go:52`); production denies all if allowlist empty (`internal/api/ws.go:56`); requests with no Origin header are allowed (`internal/api/ws.go:60`). | **VERIFY** |
| Pull limits | Pull defaults to `50`, max `100` (`internal/storeforward/engine.go:10`, `internal/storeforward/engine.go:11`, `internal/storeforward/client.go:34`). | **VERIFY** |
| Inbound federation caps | Max inbound federation connections default `100` via semaphore (`cmd/relay/main.go:48`, `cmd/relay/main.go:250`); max peers default `50` (`cmd/relay/main.go:53`, `cmd/relay/main.go:269`); rate limit default `100 req/min` by remote addr (`cmd/relay/main.go:54`, `cmd/relay/main.go:251`, `cmd/relay/main.go:276`). | **VERIFY** |
| Payload size checks | Forwarded federation payload hard-capped at 64 KiB (`internal/federation/peering.go:38`, `internal/federation/peering.go:565`); client `send` path has no explicit payload-length guard (`internal/api/ws.go:248`, `internal/api/ws.go:253`). | **VERIFY** |
| `-max-envelope-size` flag wiring | CLI exposes `-max-envelope-size` and logs it (`cmd/relay/main.go:52`, `cmd/relay/main.go:94`), but no enforcement callsite appears in relay runtime paths (repo search). | **VERIFY** |
| Sweeper cadence | Message sweeper interval default is `30s` (`cmd/relay/main.go:39`) and runs immediately on start (`internal/store/ttl_sweeper.go:38`); envelope sweeper default interval is `30s` (`internal/store/envelope_sweeper.go:19`) and begins on ticker cycle (`internal/store/envelope_sweeper.go:50`). | **VERIFY** |

## Structured summary

### Confirmed matches

1. Client `ack` is durably persisted before completion semantics (`internal/store/bbolt_store.go:185`).
2. Stored-message `expires_at` is not mutated by delivery ack writes (`internal/store/bbolt_store.go:189`).
3. Federation acceptance preserves incoming expiry value when persisting forwarded messages (`internal/storeforward/federation_forwarding.go:199`).
4. Relay keeps device-level (`ack`) and federation-level (`relay_ack`) effects separate in implementation behavior (`internal/federation/peering_test.go:380`).

### Confirmed divergences

1. Client wire shape still diverges on strict canonical identity requirements (`device_id` remains optional for backward compatibility) and error schema (`msg_id` string instead of `code`/`message`), while timestamp fields are now dual-emitted in compatibility mode.
2. Payload semantics diverge: no base64url enforcement and no canonical `EnvelopeV1` decode validation on client `send`.
3. TTL semantics diverge: default TTL tracks relay `maxTTL` instead of canonical `3600`; expired messages can still be delivered before sweep.
4. Idempotency diverges: `client_msg_id` is not represented or enforced.
5. Federation protocol diverges structurally: runtime is inventory/request/message-forward oriented rather than canonical envelope-forward protocol, though timestamp metadata is now dual-emitted via optional `envelope` fields.
6. Federation acks diverge in both semantics and shape; runtime currently processes incoming `relay_ack` but does not emit canonical accept/reject `relay_ack` responses for forwards.
7. Federation invariants diverge on wire (`hop_count`, `seen_relays`, `destination` validation absent from forwarded frame model), even with new optional envelope timestamp metadata.
8. Receipts spec is not implemented (`ReceiptV1` wrappers/scopes absent); `ack_ok` remains transport-level acknowledgment, not canonical receipt payload.

### VERIFY items

1. Non-protocol runtime limits/caps listed above should be treated as implementation details until codified in canonical contracts.
2. `-max-envelope-size` appears currently non-enforced in runtime; verify intended ownership (CLI/runtime/spec) before relying on it.
3. Federation expiry check uses strict `After` (not `>=`) and should be explicitly confirmed during alignment cutover.

### Recommended future alignment beads (prioritized)

1. **Client identity cutover bead:** Require `device_id` in `hello` at protocol cutover and remove legacy wayfarer-only fallback behavior.
2. **Client frame normalization bead:** Align `hello_ok`, `send_ok`, `message`, `messages`, and `error` fields/timestamp names to canonical v1.
3. **Encoding/idempotency bead:** Enforce base64url canonical payload handling and implement `client_msg_id` dedupe semantics.
4. **TTL semantics bead:** Switch default TTL to `3600` and prevent delivery of expired messages before sweep.
5. **Federation envelope protocol bead:** Move from inventory/request/message-forward model to canonical envelope-based `relay_forward` + invariants (`hop_count`, `seen_relays`, destination checks).
6. **Federation ack alignment bead:** Emit canonical `relay_ack(status=accepted|rejected, code?, message?)` for forward outcomes.
7. **Receipts bead:** Introduce explicit receipt wrappers (`receipt_scope`, `receipt_v1_b64`) and keep transport `ack_ok` separate from receipt payload semantics.
