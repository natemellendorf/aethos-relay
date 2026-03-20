# Gossip V1 relay profile (Option B canonical)

This document is the authoritative relay-to-relay contract for `aethos-relay`.

## 1) Scope and model

Option B canonical model:

- **Inter-domain federation:** Gossip V1 relay-to-relay exchange only.
- **Intra-domain fleet coordination:** local/private policy concern, not part of this wire contract.

Relays exchange **binary** length-prefixed Gossip V1 envelopes (`u32be length + canonical CBOR envelope`).

## 2) Canonical exchanged object semantics

Relay transfer object fields:

- `item_id`: lowercase 64-hex digest.
- `envelope_b64`: unpadded base64url canonical envelope bytes.
- `expiry_unix_ms`: absolute expiry timestamp.
- `hop_count`: unsigned hop count (`<= 65535`).

Canonical identity rule:

- `item_id == sha256(envelope bytes)` where envelope bytes are decoded from `envelope_b64`.

Canonical envelope validation expectations:

- `envelope_b64` must decode as unpadded base64url.
- Decoded bytes must be canonical CBOR encoding.
- Canonical transfer envelope fields must validate.
- Signature/domain checks must pass.

## 3) Handshake, capabilities, and policy constraints

Handshake is mandatory before non-HELLO frames:

- Relay sends HELLO.
- Peer HELLO must validate version, node identity, and bounds.
- Only after HELLO validation can SUMMARY/REQUEST/TRANSFER/RECEIPT be processed.

Core relay policy constraints:

- Max frame size and parser bounds enforced.
- Max transfer object count/bytes enforced.
- Invalid frame type is fatal for the session.
- Non-binary websocket frames are rejected.

## 4) Transfer and receipt semantics

### Transfer

On TRANSFER object ingest:

- Parse and validate each object.
- Reject malformed/invalid objects.
- Reject expired objects.
- Enforce item-id/content-address consistency.
- Persist accepted objects durably.

### Receipt

Receipt payload uses canonical `received` list (accepted item IDs only).

- Accepted IDs acknowledge what this relay accepted for this transfer.
- No legacy `accepted`/`rejected` receipt keys.
- Unknown/duplicate invalid receipt IDs are rejected by session validation.

## 5) Hop, loop, expiry, duplicate handling

- **Hop:** transfer parser rejects `hop_count > 65535`; persisted envelope hop state is tracked.
- **Loop:** source-relay seen checks suppress loop re-ingest semantics (`seen_loop`).
- **Expiry:** expired objects are rejected and not persisted.
- **Duplicate:** duplicate object ingest is idempotent (durable write skipped) and may still appear in `received` when treated as accepted duplicate.

## 6) Rejection behavior

Objects are rejected when any of these fail:

- missing/invalid `item_id`
- invalid `envelope_b64` encoding/canonical bytes
- invalid envelope schema/signature
- invalid `expiry_unix_ms`
- invalid `hop_count`
- object already expired
- policy size limits

Rejections are local decision outcomes; wire receipt remains accepted-ID list only.

## 7) Migration/deprecation: legacy `relay_forward.message`

Legacy JSON relay federation examples (`relay_forward.message`, `relay_ack`) are deprecated documentation-only artifacts and are not authoritative runtime contract.

Authoritative runtime contract is this Gossip V1 relay profile.

## 8) TAR and forwarding policy note

TAR/forwarding knobs are currently config-level only in this repo:

- parsed/validated/logged
- not wired into active relay-to-relay scheduling path

They are treated as intra-domain policy surface, not inter-domain wire contract.
