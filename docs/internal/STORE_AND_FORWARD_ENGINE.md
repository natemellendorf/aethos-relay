# Store-and-Forward Engine (Internal)

This note documents current engine behavior in the canonical Gossip V1 path.

## Scope and responsibilities

The engine currently owns:

1. Client `send` acceptance and message construction with TTL clamping.
2. Durable message persistence.
3. Client `pull` queue reads by delivery identity.
4. Client receipt/ack handling (`RECEIPT` on Gossip V1 client session) via delivery identity state.
5. Inbound federation transfer ingest for Gossip V1 `TRANSFER` objects:
   - validation
   - duplicate handling
   - expiry rejection
   - loop/seen handling
   - durable persist
6. Envelope-state persistence (id, destination, origin, hop, expiry) when envelope store is configured.
7. Relay-ingest observer signaling after durable ingest boundary.
8. Envelope sweep operations.

## Current terminology

- `message`: durable queued relay payload record.
- `transfer object`: Gossip V1 `TRANSFER.objects[]` entry.
- `item_id`: content-addressed digest (`sha256(envelope bytes)`).
- `delivery identity`: `(wayfarer_id, device_id)` binding for client ack suppression.
- `envelope state`: relay-local record for hop/seen/origin/expiry bookkeeping.
- `receipt`: Gossip V1 `RECEIPT.received[]` accepted item IDs.

## Behavior notes (as implemented)

- Client path uses canonical ack-driven suppression in runtime wiring.
- Federation ingest accepts/duplicates/seen-loop/expired outcomes through `AcceptRelayForward`.
- Expired transfer objects are rejected and not persisted.
- Duplicate transfer objects are idempotent (durable write skipped).
- Seen-by-source checks support loop suppression semantics.
- Envelope state preserves object expiry and tracks hop metadata.

## Caveats

- Seen-loop suppression is relay-local behavior, not global network guarantee.
- Pull paths may surface expired queued messages until sweep cleanup removes them.
- TAR/forwarding scheduling policy is outside this engine and currently not active in relay-to-relay scheduling.

## Package layout

`internal/storeforward` split:

- `engine.go`: engine lifecycle/config and relay-ingest observer wiring.
- `client.go`: client send/pull/ack behavior.
- `federation_forwarding.go`: transfer ingest, envelope state, seen-loop handling, envelope sweep.
