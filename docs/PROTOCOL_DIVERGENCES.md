# Protocol Divergence Audit

Last audited: 2026-03-06

This document audits current `aethos-relay` behavior against specs anchored to canonical main-branch raw URLs.

## Canonical spec sources audited

- `[CRP]` `https://raw.githubusercontent.com/natemellendorf/aethos/refs/heads/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md`
- `[FRP]` `https://raw.githubusercontent.com/natemellendorf/aethos/refs/heads/main/docs/spec/FEDERATION_PROTOCOL_V1.md`
- `[RCP]` `https://raw.githubusercontent.com/natemellendorf/aethos/refs/heads/main/docs/spec/RECEIPTS.md`
- `[MIG]` `https://raw.githubusercontent.com/natemellendorf/aethos/refs/heads/main/docs/migration/protocol_update.md` (planning input; not a normative wire contract)

## Audit Re-run Using Canonical GitHub Specs

- What changed vs prior local-path-based audit: this pass is anchored to canonical raw URLs (`[CRP]`, `[FRP]`, `[RCP]`, `[MIG]`), all status labels are normalized to `MATCHES SPEC` / `DIVERGES FROM SPEC` / `VERIFY`, and evidence references are updated to current files in this checkout.
- What remained correct: core divergence themes are unchanged (missing client `device_id` semantics, legacy client frame fields, federation inventory/request/message flow instead of canonical envelope-forwarding, and missing receipts wrappers).
- New findings discovered in this re-run: (1) canonical `wayfarer_id` format (`[0-9a-f]{64}`) is not validated in `hello`; (2) federation `relay_hello` requires integer `protocol_version=1`, but runtime uses string `version="1.0"`; (3) envelope expiry checks use strict `time.After(...)` semantics at envelope helpers, which is not the canonical `now >= expires_at` boundary in `[FRP]`.

Time-unit note: `[CRP]` uses Unix epoch **seconds** (`received_at`, `expires_at`) while `[FRP]` uses Unix epoch **milliseconds** (`created_at`, `expires_at`, `sent_at`).

## Client <-> Relay audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| `hello` required identity fields | `[CRP]` requires `hello.wayfarer_id` and `hello.device_id`. | `WSFrame` has `wayfarer_id` but no `device_id` (`internal/model/message.go:35`); `handleHello` only validates `wayfarer_id` (`internal/api/ws.go:218`). | DIVERGES FROM SPEC |
| `wayfarer_id` format validation | `[CRP]` requires lowercase 64-char hex `wayfarer_id`. | `handleHello` checks only non-empty `wayfarer_id` (`internal/api/ws.go:219`). | DIVERGES FROM SPEC |
| Per-device delivery/ack binding | `[CRP]` requires delivery+ack state by `(wayfarer_id, device_id)`. | Engine/store can accept device-qualified identities via `DeliveryIdentity(wayfarerID, deviceID)` (`internal/storeforward/client.go:16`) and persist opaque recipient strings in delivery keys (`internal/store/codec.go:170`), but WS frames/handlers never populate `device_id` and currently key by `wayfarer_id` only (`internal/api/ws.go:225`, `internal/api/ws.go:299`, `internal/api/ws.go:323`), so acks suppress across devices for the same wayfarer. | DIVERGES FROM SPEC |
| `hello_ok` fields | `[CRP]` requires `hello_ok` with `relay_id`. | `handleHello` emits only `{ "type": "hello_ok" }` (`internal/api/ws.go:230`). | DIVERGES FROM SPEC |
| `send` acceptance invariants | `[CRP]` requires canonical payload decode, `to` consistency, authz, durable persist before `send_ok`. | `handleSend` enforces auth + non-empty `to` + non-empty `payload_b64` and persists before `send_ok` (`internal/api/ws.go:239`, `internal/api/ws.go:259`, `internal/api/ws.go:271`), but no canonical payload decode or `TO_MISMATCH` check is visible in this path. | DIVERGES FROM SPEC |
| `send_ok` fields | `[CRP]` defines `send_ok.msg_id` and optional paired `received_at`+`expires_at`. | Runtime returns `send_ok` with `msg_id` and legacy `at` (`internal/api/ws.go:272`, `internal/model/message.go:45`). | DIVERGES FROM SPEC |
| Push `message` shape | `[CRP]` requires `message.received_at` (seconds) with canonical naming. | Runtime emits `message.at` (`internal/api/ws.go:371`, `internal/model/message.go:45`). | DIVERGES FROM SPEC |
| Pull `messages` item shape | `[CRP]` requires `msg_id`, `from`, `payload_b64`, `received_at`. | Pull serializes `model.Message` with legacy `at` plus additional fields (`to`, `expires_at`, `delivered`) (`internal/api/ws.go:331`, `internal/model/message.go:9`). | DIVERGES FROM SPEC |
| Pull defaults and limits | `[CRP]` default limit is `50` when omitted. | Engine defines `defaultPullLimit=50` and `maxPullLimit=100` (`internal/storeforward/engine.go:10`); helper applies default `50` for omitted/invalid limits (`internal/storeforward/client.go:33`); WS pull path uses that helper (`internal/api/ws.go:322`). | MATCHES SPEC |
| `ack` durability before completion | `[CRP]` requires durable per-device state transition before completion semantics. | `handleAck` performs store write path before returning `ack_ok` (`internal/api/ws.go:299`, `internal/api/ws.go:309`); store writes delivery state inside a bbolt update transaction (`internal/store/bbolt_store.go:169`). | MATCHES SPEC |
| `ack_ok` transport semantics | `[CRP]` defines `ack_ok` as response frame with `msg_id`. | Runtime responds with `ack_ok` + `msg_id` (`internal/api/ws.go:309`). | MATCHES SPEC |
| `error` frame schema/codes | `[CRP]` requires `error.code` and `error.message`. | `sendError` puts text in `msg_id`; `WSFrame` has no `code` or `message` fields (`internal/api/ws.go:407`, `internal/model/message.go:35`). | DIVERGES FROM SPEC |
| `payload_b64` canonical encoding | `[CRP]` requires base64url (no padding) and canonical envelope bytes. | WS path validates only non-empty payload (`internal/api/ws.go:248`) and persists payload as provided (`internal/store/codec.go:31`, `internal/store/bbolt_store.go:93`). | DIVERGES FROM SPEC |
| Idempotency via `client_msg_id` | `[CRP]` requires dedupe by `(sender_wayfarer_id, client_msg_id)` when present. | `WSFrame` has no `client_msg_id` field (`internal/model/message.go:35`) and no idempotency branch in `handleSend` (`internal/api/ws.go:239`). | DIVERGES FROM SPEC |
| TTL default and expiry-delivery boundary | `[CRP]` default requested TTL is `3600`; expired messages (`now >= expires_at`) MUST NOT be delivered. | Omitted `ttl_seconds` decodes to `0` on WS frames (`internal/model/message.go:40`), and send handling maps `ttl_seconds <= 0` to `maxTTL` (`internal/storeforward/client.go:43`) instead of canonical `3600`. Pull handling also documents that expired messages may be returned until cleanup (`internal/storeforward/client.go:67`), and WS delivery still emits frames for those messages (remaining TTL is clamped to `0` but the message is sent) (`internal/api/ws.go:366`, `internal/api/ws.go:371`). | DIVERGES FROM SPEC |
| `expires_at` immutability | `[CRP]` requires immutable `expires_at` once accepted. | `MarkDelivered` mutates delivery metadata and does not modify message expiry (`internal/store/bbolt_store.go:169`, `internal/store/bbolt_store.go:189`). | MATCHES SPEC |

## Federation audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| Federation wire model | `[FRP]` defines `relay_forward(envelope)` + `relay_ack` + `relay_cover` model. | Runtime uses inventory/request/message-forward flow (`internal/model/message.go:63`, `internal/model/message.go:64`, `internal/model/message.go:92`, `internal/federation/peering.go:478`, `internal/federation/peering.go:520`, `internal/federation/peering.go:554`). | DIVERGES FROM SPEC |
| `relay_hello` shape | `[FRP]` requires `protocol_version` integer `1`. | Frame model uses `version` string (`internal/model/message.go:75`), constant is `"1.0"` (`internal/federation/peering.go:22`), inbound validation checks only type+relay_id (`internal/federation/peering.go:827`). | DIVERGES FROM SPEC |
| `relay_forward` payload shape | `[FRP]` requires `envelope` object with canonical envelope fields. | `RelayForwardFrame` carries `message` (`internal/model/message.go:92`), and forward path serializes/deserializes `message` form (`internal/federation/peering.go:356`, `internal/federation/peering.go:896`). | DIVERGES FROM SPEC |
| `relay_ack` status/model | `[FRP]` requires `status in {accepted,rejected}` plus `code/message` for rejection. | `RelayAckFrame` contains `destination` and free-form status (`internal/model/message.go:98`); handler accepts `accepted|duplicate|expired` (`internal/federation/peering.go:625`), and no canonical reject-ack emission is present in forward handler (`internal/federation/peering.go:554`). | DIVERGES FROM SPEC |
| Hop-count enforcement and increment | `[FRP]` requires reject on `hop_count >= MAX_HOPS` and increment by 1 before forwarding. | `MAX_HOPS` exists in envelope model (`internal/model/envelope.go:61`), but runtime wire forward frame has no hop field and forward path does not enforce/increment it (`internal/model/message.go:92`, `internal/federation/peering.go:554`, `internal/federation/peering.go:896`). | DIVERGES FROM SPEC |
| `seen_relays` loop prevention | `[FRP]` requires append local relay and reject if already seen. | Runtime frame model has no `seen_relays` field (`internal/model/message.go:92`); only envelope-store seen index helpers exist (`internal/store/envelope_store.go:227`, `internal/store/envelope_store.go:235`). | DIVERGES FROM SPEC |
| Destination/payload consistency checks | `[FRP]` requires `destination` to match decoded payload destination and `envelope_id = SHA-256(payload)`. | `handleRelayForward` validates message presence/basic fields only and does not decode canonical envelope payload or verify envelope hash invariants (`internal/federation/peering.go:559`). | DIVERGES FROM SPEC |
| `expires_at` immutability across hops | `[FRP]` forbids TTL extension. | Forward path passes existing message timestamps through to persistence path and no explicit expiry-extension write is visible in these files (`internal/federation/peering.go:575`, `internal/store/bbolt_store.go:108`). | VERIFY |
| Expiry rejection boundary | `[FRP]` requires expired when `now_ms >= expires_at`. | Envelope helper uses `time.Now().After(expires_at)` (strict `>` semantics) (`internal/model/envelope.go:55`), and envelope-store read path relies on that helper (`internal/store/envelope_store.go:147`). | DIVERGES FROM SPEC |
| `relay_cover` required fields/time units | `[FRP]` requires `relay_id` and `sent_at` (Unix ms). | Cover frame uses `ts` + `nonce` (`internal/model/message.go:107`), sender uses `time.Now().Unix()` seconds (`internal/federation/tar.go:183`), receiver ignores cover payload (`internal/federation/peering.go:369`). | DIVERGES FROM SPEC |

## Receipts audit

| Item | Canonical requirement | Current relay behavior (evidence) | Status |
| --- | --- | --- | --- |
| Receipt wrapper support | `[RCP]` JSON channels require `receipt_scope` and `receipt_v1_b64` wrapper where mixed scopes can appear. | Client/federation frame models do not define wrapper fields (`internal/model/message.go:35`, `internal/model/message.go:98`), and WS/federation handlers do not parse such wrappers (`internal/api/ws.go:193`, `internal/federation/peering.go:336`). | DIVERGES FROM SPEC |
| Non-conflation of device vs federation semantics | `[RCP]` requires device and federation semantics to remain distinct. | Client `ack` updates delivery state (`internal/api/ws.go:299`), while `relay_ack` updates peer metrics only (`internal/federation/peering.go:616`). | MATCHES SPEC |
| `ack_ok` vs receipt semantics | `[CRP]` defines `ack_ok` transport response; `[RCP]` keeps `ReceiptV1` semantics separate. | Runtime keeps transport `ack_ok` as WS response (`internal/api/ws.go:309`) and does not treat it as `ReceiptV1` wrapper payload (`internal/model/message.go:35`). | MATCHES SPEC |

## Implementation Notes (non-protocol constraints)

These are implementation/runtime limits and policies; they are not canonical protocol contracts.

| Item | Current behavior (evidence) | Status |
| --- | --- | --- |
| WebSocket buffer sizes | Client upgrader uses `1024/1024` (`internal/api/ws.go:116`, `internal/api/ws.go:117`); federation upgrader uses `1024/1024` (`internal/federation/peering.go:795`, `internal/federation/peering.go:796`). | VERIFY |
| Backpressure buffers | Client send queue `256` + 1s enqueue timeout (`internal/api/ws.go:132`, `internal/api/ws.go:400`); federation peer send queue `256` (`internal/federation/peering.go:223`, `internal/federation/peering.go:812`); TAR batch queue `256` (`internal/federation/peering.go:415`). | VERIFY |
| Origin policy | Dev mode allows all (`internal/api/ws.go:52`); empty allowlist denies (`internal/api/ws.go:56`); missing `Origin` header is allowed (`internal/api/ws.go:60`). | VERIFY |
| Pull limit normalization details | WS pull path uses normalization helper (`internal/api/ws.go:322`) with `defaultPullLimit=50` and `maxPullLimit=100` constants (`internal/storeforward/engine.go:10`, `internal/storeforward/client.go:33`). | VERIFY |
| Inbound federation caps | Defaults: inbound conns `100` (`cmd/relay/main.go:48`), max peers `50` (`cmd/relay/main.go:53`), rate limit `100 req/min` (`cmd/relay/main.go:54`), wired at handler (`cmd/relay/main.go:250`, `cmd/relay/main.go:269`, `cmd/relay/main.go:276`). | VERIFY |
| Payload size checks | Federation forward payload capped at 64 KiB (`internal/federation/peering.go:38`, `internal/federation/peering.go:565`); client send path has no explicit byte-length guard (`internal/api/ws.go:248`). | VERIFY |
| `-max-envelope-size` flag wiring | CLI exposes and logs `-max-envelope-size` (`cmd/relay/main.go:52`, `cmd/relay/main.go:94`), but no enforcement use is visible in audited runtime paths. | VERIFY |
| Sweeper cadence | Message sweeper interval comes from `-sweep-interval` (`cmd/relay/main.go:39`) and runs immediately on start (`internal/store/ttl_sweeper.go:38`); envelope sweeper defaults to `30s` (`internal/store/envelope_sweeper.go:19`) and starts on ticker cadence (`internal/store/envelope_sweeper.go:50`). | VERIFY |

## Structured summary

### Confirmed matches

1. Client `ack` persists delivery state before `ack_ok` response (`internal/api/ws.go:299`, `internal/store/bbolt_store.go:169`).
2. `expires_at` is not mutated by delivery-state updates (`internal/store/bbolt_store.go:189`).
3. Device-level ack and relay-level ack remain semantically separated (`internal/api/ws.go:299`, `internal/federation/peering.go:616`).

### Confirmed divergences

1. Client handshake/identity diverges: missing `device_id`, no strict `wayfarer_id` format validation, and missing `relay_id` in `hello_ok`.
2. Client frame schema diverges: legacy `at` naming, non-canonical `error` shape, and no `client_msg_id` idempotency support.
3. Client payload invariants diverge: no visible canonical base64url/decode/to-mismatch enforcement in WS path.
4. Client TTL semantics diverge: omitted `ttl_seconds` defaults to `maxTTL`, not canonical `3600`.
5. Client expiry boundary diverges: expired queued messages may still be returned/delivered until cleanup.
6. Federation wire protocol diverges structurally from canonical envelope-forward model.
7. Federation invariants diverge: hello version field shape, ack status model, hop/seen fields, destination/hash checks, and cover-frame fields/time units.
8. Receipt wrappers are not implemented for mixed-scope receipt transport.

### VERIFY items to emphasize

1. Federation `expires_at` immutability across hops still needs stronger end-to-end write-path evidence.
2. `-max-envelope-size` is exposed as a flag but no enforcement callsite is visible in audited runtime paths.
