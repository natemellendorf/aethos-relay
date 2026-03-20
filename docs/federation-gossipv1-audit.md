# Federation Gossip V1 canonicalization audit

- Audit date: 2026-03-20
- Scope: `aethos-relay` relay-to-relay behavior vs Gossip V1 relay profile (Option B)

## Summary

This audit confirms that relay-to-relay implementation is Gossip V1 binary-first and content-addressed, while docs had legacy contradictions that are now corrected in this bead.

The main remaining implementation gap is not protocol correctness; it is policy wiring: TAR/forwarding knobs are present but not active in relay-to-relay scheduling.

## Implementation-vs-contract matrix

| Area | Canonical contract (Option B) | Implementation status | Notes |
| --- | --- | --- | --- |
| Transport framing | Binary length-prefixed Gossip V1 envelopes | **Aligned** | `internal/federation/peering.go`, `internal/gossipv1/session.go` |
| Handshake | HELLO with validated version/node identity/caps | **Aligned** | `gossipv1.ParseHelloPayload` + `ValidateHello` |
| Object semantics | `TRANSFER.objects[]` with `item_id = sha256(canonical envelope bytes)` | **Aligned** | `ParseTransferPayloadMixed`, `ComputeTransferObjectItemID` |
| Canonical envelope bytes | Canonical CBOR envelope bytes in `envelope_b64` | **Aligned** | strict base64url + canonical CBOR re-encode checks |
| Request semantics | `SUMMARY` → `REQUEST.want[]` by missing IDs | **Aligned** | want computation + preview cursor checks |
| Receipt semantics | `RECEIPT.received[]` accepted IDs only | **Aligned** | no legacy `accepted`/`rejected` fields on wire |
| Duplicate handling | Idempotent duplicate acceptance behavior | **Aligned** | duplicate returns accepted receipt ID in current behavior; durable write skipped |
| Expiry handling | Expired transfer objects rejected | **Aligned** | `AcceptRelayForward` returns expired status; no persist |
| Hop handling | Transfer with invalid hop_count rejected; persisted hop tracked in envelope store | **Aligned** | hop_count parser gate (`<= 65535`) and envelope persistence |
| Loop suppression | Seen-by-source check prevents loop re-ingest semantics | **Aligned** | `RelayForwardSeenLoop` path in engine |
| Legacy JSON relay frames | Deprecated / non-authoritative | **Removed from authoritative docs** | legacy `relay_forward.message` / `relay_ack` language cleaned up |
| TAR/forwarding policy knobs | Separate intra-domain policy concern | **Partially wired (config only)** | parsed/validated/logged, not active in relay-to-relay scheduling |

## Contradictions found and resolved

1. **README federation section used legacy JSON `relay_forward`/`relay_ack` framing examples** while implementation uses Gossip V1 binary frames.
   - Resolved by replacing README federation protocol section with Gossip V1 relay profile references.

2. **Conformance/divergence docs stated active divergences to legacy frame shapes** (`relay_forward.message`, `relay_ack` statuses) that no longer represent runtime behavior.
   - Resolved by rewriting protocol conformance path around Option B canonical profile.

3. **Internal store-forward engine doc used old relay frame terminology** that no longer matched transport behavior.
   - Resolved by updating terminology to TRANSFER/RECEIPT and canonical object semantics.

4. **TAR/forwarding knobs appeared operationally active in docs** but are presently not connected to active relay-to-relay scheduling logic.
   - Resolved by explicit de-scope language in docs and CLI flag descriptions/log note.

## TAR/forwarding strategy status (truth-in-reality)

- `TARConfig` / `ForwardingConfig` are parsed, validated, and logged in `cmd/relay/main.go`.
- `PeerBatcher`, `PadPayload`, and `ForwardingStrategy` have unit tests in `internal/federation/*_test.go`.
- Current `PeerManager` relay-to-relay flow uses direct summary/request/transfer exchange and does not wire TAR batcher/padding/cover or score-based top-k/explore selection into active scheduling.

## Legacy model migration/deprecation state

- `relay_forward.message` and JSON `relay_ack` are treated as legacy documentation-only model and are no longer authoritative for relay-to-relay contract in this repo.
- Authoritative contract path is now Option B Gossip V1 relay profile docs under `docs/federation/` and `docs/PROTOCOL_CONFORMANCE.md`.
