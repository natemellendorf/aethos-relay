# Protocol Conformance

This relay implementation targets the canonical protocol specifications defined in the `aethos` repository, with known implementation deviations listed below.

- [Spec index](https://github.com/natemellendorf/aethos/tree/main/docs/spec)
- [Client Relay Protocol v1](https://github.com/natemellendorf/aethos/blob/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md)
- [Federation Protocol v1](https://github.com/natemellendorf/aethos/blob/main/docs/spec/FEDERATION_PROTOCOL_V1.md)

## Supported Protocol Versions

- Client Relay Protocol: v1
- Federation Protocol: v1

## Known Deviations (Non-Normative)

- `relay_forward` canonical frame shape is `{"type":"relay_forward","envelope":{...}}` in [FEDERATION_PROTOCOL_V1.md](https://github.com/natemellendorf/aethos/blob/main/docs/spec/FEDERATION_PROTOCOL_V1.md), while current relay code uses `RelayForwardFrame` with `json:"message"` (`internal/model/message.go`) and corresponding marshal/unmarshal paths in `internal/federation/peering.go`.
- `relay_hello` canonical field is `protocol_version` integer (v1 spec), while current relay code sends/accepts string `version` via `RelayHelloFrame` (`internal/model/message.go`) and `ProtocolVersion = "1.0"` (`internal/federation/peering.go`).
- `relay_ack` canonical statuses are `accepted|rejected` (with optional `code`/`message`), while current relay code handles `accepted|duplicate|expired` in `handleRelayAck` (`internal/federation/peering.go`).

## Implementation Constraints (Non-Normative)

These are implementation limits and defaults in `aethos-relay`. They are not the canonical protocol source of truth.

| Constraint | Default | Runtime Configuration | Source Location |
| --- | --- | --- | --- |
| Max envelope payload size | `65536` bytes | `-max-envelope-size` | `cmd/relay/main.go` |
| Forwarded payload hard limit | `64 * 1024` bytes | Compile-time constant | `internal/federation/peering.go` (`MaxForwardedPayloadSize`) |
| Max federation peers | `50` | `-max-federation-peers` | `cmd/relay/main.go`; `internal/model/envelope.go` (`MAX_FEDERATION_PEERS`) |
| Max inbound federation connections | `100` | `-max-federation-conns` | `cmd/relay/main.go` |
| Federation rate limit per peer | `100` requests/minute | `-rate-limit-per-peer` (window is `time.Minute`) | `cmd/relay/main.go`; `internal/federation/peering.go` (`RateLimiter`) |
| Federation batch interval | `500ms` | `-federation-batch-interval` | `cmd/relay/main.go`; `internal/federation/tar.go` (`TARConfig`) |
| Federation batch jitter | `250ms` | `-federation-batch-jitter` | `cmd/relay/main.go`; `internal/federation/tar.go` (`TARConfig`) |
| Federation batch max frames | `10` | `-federation-batch-max` | `cmd/relay/main.go`; `internal/federation/tar.go` (`TARConfig`) |

## How To Discover Effective Values

- CLI defaults and descriptions: run `go run ./cmd/relay/main.go -h`
- Runtime startup values: inspect relay startup logs from `cmd/relay/main.go`
- Constant/fallback behavior: review `internal/federation/tar.go`, `internal/federation/peering.go`, and `internal/model/envelope.go`

Environment variables are not currently used for these protocol constraint values.
