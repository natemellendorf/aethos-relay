# Store-and-Forward Engine (Internal)

This note documents the relay's current store-and-forward behavior after extracting shared logic into `internal/storeforward`.

## Scope and Responsibilities

The engine currently owns these responsibilities:

1. Accept client `send` input and build a persisted message with relay-side TTL clamping.
2. Persist queued messages in the message store for durability.
3. Serve client `pull` by reading queued messages for a destination identity and filtering expired entries at read time.
4. Record client delivery acknowledgements (`ack`) as delivery-state entries for the connection identity used by the caller.
5. Track per-delivery-identity delivery state so one identity acknowledgement does not automatically acknowledge another identity.
6. Handle inbound federation forwards (`relay_forward`, currently carrying `message` in this implementation) with validation, duplicate checks, expiry checks, and persistence.
7. Maintain federation envelope state (ID, destination, origin, hop count, expiry) when an envelope store is configured.
8. Select/prepare forwarding candidates by reserving `(envelope_id, relay_id)` seen markers before sending to a peer.
9. Process relay-to-relay receipts (`relay_ack`) as forwarding telemetry only.
10. Sweep expired envelopes from the envelope store when requested.

The engine does not change wire frame names, field names, or frame encodings. Client and federation handlers still marshal/unmarshal the same frame types as before.

## Current Terminology

- `message`: persisted relay payload currently transported in client `message/messages` frames and federation `relay_forward.message`.
- `queued message`: persisted message not yet marked delivered for a queried delivery identity.
- `device delivery`: delivery-state marker keyed by `(msg_id, delivery_identity)`.
- `envelope`: internal federation record used for hop count, seen tracking, origin metadata, and expiry.
- `federation receipt`: `relay_ack` status from one relay to another about forwarding acceptance/handling.
- `expiry`: absolute `expires_at` timestamp carried by message/envelope and used to drop/sweep stale data.
- `forwarding candidate`: peer relay selected by forwarding strategy and accepted by seen-tracking reservation.

## Behavior Notes (As Implemented)

- Client `send` always persists first; online recipients are then delivered from persisted state.
- Client `ack` is delivery acknowledgement for the client/identity path invoking `ack` (not a federation receipt).
- Federation `relay_ack` is relay-to-relay envelope/message handling telemetry and does not mark client delivery state.
- Federation forwarding preserves the original message expiry when deriving/storing envelope state.
- Hop count is tracked in internal envelope state and incremented per outbound forward attempt; configured max hop limits are enforced when envelope state is available.
- Seen tracking is recorded as `(envelope_id, relay_id)` and used to avoid forwarding loops to the same relay.
- Inbound forwarded payloads are dropped when malformed, oversized, duplicate, already expired, or already seen from that relay.
- Expired messages are filtered out on pull responses, and expired envelopes are removable via sweep operations.

## Package Layout

`internal/storeforward` is intentionally split by semantic layer:

- `engine.go`: engine wiring/state and federation configuration.
- `client.go`: client send/pull/ack behavior and delivery identity handling.
- `federation_forwarding.go`: relay forward acceptance, envelope hop/seen handling, and envelope sweeps.
- `receipts.go`: relay receipt (`relay_ack`) telemetry tracking.
