# Protocol Conformance

This relay targets canonical `aethos` protocol behavior with Option B federation profile.

- [Spec index](https://github.com/natemellendorf/aethos/tree/main/docs/spec)
- [Client Relay Protocol v1](https://github.com/natemellendorf/aethos/blob/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md)
- [Federation Protocol v1](https://github.com/natemellendorf/aethos/blob/main/docs/spec/FEDERATION_PROTOCOL_V1.md)

## Supported Protocol Versions

- Client Relay Protocol: v1
- Federation Protocol: v1 (Gossip V1 relay profile)

## Authoritative relay-to-relay contract in this repo

- [docs/federation/GOSSIPV1_RELAY_PROFILE.md](federation/GOSSIPV1_RELAY_PROFILE.md)
- [docs/federation/CONFORMANCE_VECTORS.md](federation/CONFORMANCE_VECTORS.md)
- [docs/federation-gossipv1-audit.md](federation-gossipv1-audit.md)
- [docs/adr/0001-gossipv1-federation-option-b.md](adr/0001-gossipv1-federation-option-b.md)

## Conformance status

### Client path

- Canonical Gossip V1 frame flow and strict canonical payload handling are enforced in the migrated path.
- Client delivery suppression behavior is canonical ack-driven in runtime wiring.

### Relay-to-relay federation path

- Canonical path is Gossip V1 binary HELLO/SUMMARY/REQUEST/TRANSFER/RECEIPT.
- Canonical transfer object semantics are enforced (`item_id` content-address, canonical envelope bytes, strict parse gates).
- Receipt semantics are canonical `received` accepted-ID list only.
- Sessions support **multi-round drain behavior** on one authenticated connection: a single session may run multiple `SUMMARY -> REQUEST -> TRANSFER -> RECEIPT` rounds until drained or stopped/yielded by configured budgets.
- **Wire schema is unchanged** by multi-round drain: no new frame types, and HELLO/SUMMARY/REQUEST/TRANSFER/RECEIPT payload field names and meanings remain canonical.

Legacy JSON relay federation examples (`relay_forward.message` / `relay_ack`) are deprecated and non-authoritative for this repository.

## Implementation constraints (non-normative)

| Constraint | Default | Runtime Configuration | Source Location |
| --- | --- | --- | --- |
| Max envelope payload size | `65536` bytes | `-max-envelope-size` | `cmd/relay/main.go` |
| Max federation peers | `50` | `-max-federation-peers` | `cmd/relay/main.go`; `internal/model/envelope.go` |
| Max inbound federation connections | `100` | `-max-federation-conns` | `cmd/relay/main.go` |
| Federation rate limit per peer | `100` requests/minute | `-rate-limit-per-peer` | `cmd/relay/main.go`; `internal/federation/peering.go` |
| TAR batch interval (config surface) | `500ms` | `-federation-batch-interval` | `cmd/relay/main.go`; `internal/federation/tar.go` |
| TAR batch jitter (config surface) | `250ms` | `-federation-batch-jitter` | `cmd/relay/main.go`; `internal/federation/tar.go` |
| TAR batch max (config surface) | `10` | `-federation-batch-max` | `cmd/relay/main.go`; `internal/federation/tar.go` |

### TAR/forwarding reality note

TAR and score-based forwarding strategy knobs are currently parsed/validated/logged but not wired into active relay-to-relay Gossip V1 scheduling.

## How to discover effective values

- CLI defaults and descriptions: `go run ./cmd/relay/main.go -h`
- Runtime startup values: relay startup logs from `cmd/relay/main.go`
- Persisted-state visibility: startup inventory log line
