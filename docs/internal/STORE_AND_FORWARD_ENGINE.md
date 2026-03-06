# Store-and-Forward Engine (Internal)

This note documents the relay's current store-and-forward behavior after extracting shared logic into `internal/storeforward`.

## Scope and Responsibilities

The engine currently owns these responsibilities:

1. Accept client `send` input and build a message with relay-side TTL clamping.
2. Persist queued messages in the message store.
3. Serve client `pull` by reading queued messages for a destination identity using store semantics.
4. Record client delivery acknowledgements (`ack`) as delivery-state entries for the connection identity used by the caller.
5. Track per-delivery-identity delivery state so one identity acknowledgement does not automatically acknowledge another identity.
6. Handle inbound federation forwards (`relay_forward`, currently carrying `message` in this implementation) with validation, duplicate checks, expiry checks, and persistence.
7. Maintain federation envelope state (ID, destination, origin, hop count, expiry) when an envelope store is configured.
8. Provide optional helpers for envelope hop/seen state when an envelope store is explicitly configured.
9. Process relay-to-relay receipts (`relay_ack`) as forwarding telemetry only.
10. Sweep expired envelopes from the envelope store when requested.

The engine does not change wire frame names, field names, or frame encodings. Client and federation handlers still marshal/unmarshal the same frame types as before.

## Current Terminology

- `message`: persisted relay payload currently transported in client `message/messages` frames and federation `relay_forward.message`.
- `queued message`: persisted message not yet marked as delivered for a queried delivery identity.
- `device delivery`: delivery-state marker keyed by `(msg_id, delivery_identity)`.
- `envelope`: internal federation record used for hop count, seen tracking, origin metadata, and expiry.
- `federation receipt`: `relay_ack` status from one relay to another about forwarding acceptance/handling.
- `expiry`: absolute `expires_at` timestamp carried by message/envelope and used to drop/sweep stale data.
- `forwarding candidate`: peer relay selected by the existing forwarding strategy.

## Behavior Notes (As Implemented)

- Client `send` persists first; online recipients are then attempted via push from persisted state.
- Client `ack` marks delivery state for the client/identity path invoking `ack` (not a federation receipt).
- Federation `relay_ack` is relay-to-relay envelope/message handling telemetry and does not mark client delivery state.
- Federation forwarding preserves the original message expiry when deriving/storing envelope state.
- The current peer forwarding path keeps origin behavior: no hop-limit enforcement is applied during `ForwardToPeers`.
- Inbound `relay_forward` keeps origin validation gates: malformed, oversized, duplicate, or expired payloads are dropped.
- Pulled queues can include expired messages until normal message TTL cleanup removes them.
- Expired envelopes are removable via envelope sweep operations.

## Caveats

- Delivery-state is relay-local bookkeeping, not end-to-end receipt or app-layer processing confirmation.
- Identity/auth context is established by API/federation handlers; the engine does not authenticate principals.
- Loop suppression from seen-tracking is best-effort relay behavior, not a protocol-level guarantee.
- Push delivery can drop frames under connection backpressure; `pull` is the recovery path.
- `relay_ack` is telemetry/scoring input and does not currently drive retry policy or delivery correctness.

## Package Layout

`internal/storeforward` is intentionally split by semantic layer:

- `engine.go`: engine wiring/state and federation configuration.
- `client.go`: client send/pull/ack behavior and delivery identity handling.
- `federation_forwarding.go`: relay forward acceptance, envelope hop/seen handling, and envelope sweeps.
- `receipts.go`: relay receipt (`relay_ack`) telemetry tracking.
