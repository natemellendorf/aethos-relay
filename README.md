<p align="center">
  <img src="https://raw.githubusercontent.com/natemellendorf/aethos/refs/heads/main/docs/img/banner.jpg" alt="Aethos banner" width="960">
</p>

# aethos-relay

WebSocket relay server for Aethos with message queuing, TTL persistence, and relay federation.

## Features

- WebSocket-based real-time messaging
- Persistent message queue with TTL support
- Per-device delivery tracking (messages delivered to one device won't be suppressed for other devices of the same user)
- Configurable WebSocket origin policy for production security
- **Multi-relay federation** - Connect to other relays for mesh networking
- **TTL persistence** - Messages persist until expiration or delivery
- **Health monitoring** - Built-in health check and metrics endpoints

## Quick Start

### Run a Single Relay

```bash
go run ./cmd/relay/main.go -ws-addr :8080 -http-addr :8081
```

This starts a relay with WebSocket on `:8080` and HTTP on `:8081`.

List all runtime flags and defaults:

```bash
go run ./cmd/relay/main.go -h
```

### Common Launch Profiles

Development with verbose Gossip V1 diagnostics:

```bash
go run ./cmd/relay/main.go \
  -dev-mode \
  -gossipv1-debug \
  -ws-addr :8002 \
  -http-addr :8001
```

Production-style origin allowlist:

```bash
go run ./cmd/relay/main.go \
  -allowed-origins "https://app.aethos.io,https://aethos.app" \
  -ws-addr :8080 \
  -http-addr :8081
```

Persistent local paths (explicit store files):

```bash
go run ./cmd/relay/main.go \
  -store-path ./relay.db \
  -envelope-store-path ./relay.db.envelopes \
  -descriptor-store-path ./relay.db.descriptors
```

Federation-enabled relay:

```bash
go run ./cmd/relay/main.go \
  -relay-id relay-us-east \
  -peer "ws://relay-1.example.com:8081/federation/ws,ws://relay-2.example.com:8081/federation/ws" \
  -max-federation-conns 200 \
  -max-federation-peers 100 \
  -rate-limit-per-peer 200
```

### Run with Docker

Build the image locally:

```bash
docker build -t aethos-relay:local .
```

Run the relay with persisted storage:

```bash
docker run --rm \
  -p 8080:8080 \
  -p 8081:8081 \
  -v aethos-relay-data:/var/lib/aethos \
  aethos-relay:local
```

The container starts `relay` with:
- `-ws-addr=:8080`
- `-http-addr=:8081`
- `-store-path=/var/lib/aethos/relay.db`

Override flags by appending them to `docker run`:

```bash
docker run --rm -p 8081:8081 aethos-relay:local -http-addr=:8081 -dev-mode
```

Run with Docker Compose:

```bash
docker compose up --build
```

Stop and remove the stack:

```bash
docker compose down
```

### Container Releases (GHCR)

The repo includes a release workflow that publishes multi-arch images to GitHub Container Registry (GHCR) on tag pushes (`v*`):

- `ghcr.io/natemellendorf/aethos-relay:<tag>`
- `ghcr.io/natemellendorf/aethos-relay:sha-<commit>`

Pull and run a published image:

```bash
docker pull ghcr.io/natemellendorf/aethos-relay:v0.1.0
docker run --rm -p 8080:8080 -p 8081:8081 ghcr.io/natemellendorf/aethos-relay:v0.1.0
```

### Run with Federation

Connect to peer relays for mesh networking:

```bash
go run ./cmd/relay/main.go \
  -relay-id "relay-us-east" \
  -peer "ws://relay-1.example.com:8081/federation/ws,ws://relay-2.example.com:8081/federation/ws" \
  -http-addr :8081
```

## Supported Protocol Versions

- Client Relay Protocol: v1 ([CLIENT_RELAY_PROTOCOL_V1.md](https://github.com/natemellendorf/aethos/blob/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md))
- Federation Protocol: v1 ([FEDERATION_PROTOCOL_V1.md](https://github.com/natemellendorf/aethos/blob/main/docs/spec/FEDERATION_PROTOCOL_V1.md))
- Relay conformance notes: [docs/PROTOCOL_CONFORMANCE.md](docs/PROTOCOL_CONFORMANCE.md)

The canonical specification for wire protocol fields and semantics is defined in the aethos protocol repository.
See [aethos/docs/spec](https://github.com/natemellendorf/aethos/tree/main/docs/spec).

### Relay federation authoritative docs (this repository)

- [docs/federation/GOSSIPV1_RELAY_PROFILE.md](docs/federation/GOSSIPV1_RELAY_PROFILE.md)
- [docs/federation/CONFORMANCE_VECTORS.md](docs/federation/CONFORMANCE_VECTORS.md)
- [docs/federation-gossipv1-audit.md](docs/federation-gossipv1-audit.md)
- [docs/adr/0001-gossipv1-federation-option-b.md](docs/adr/0001-gossipv1-federation-option-b.md)

## Flags

Boolean flags can be passed as either `-flag` or `-flag=true|false`.

### Server Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-ws-addr` | `:8080` | WebSocket server address |
| `-http-addr` | `:8081` | HTTP server address |
| `-store-path` | `./relay.db` | Path to bbolt database |
| `-sweep-interval` | `30s` | TTL sweeper interval |
| `-max-ttl-seconds` | `604800` | Maximum TTL in seconds (default 7 days) |
| `-log-json` | `false` | JSON logging output |
| `-allowed-origins` | (empty) | Comma-separated list of allowed WebSocket origins |
| `-dev-mode` | `false` | Enable development mode (allows all origins) |
| `-ack-driven-suppression` | `false` | Use canonical ack-driven delivery suppression (legacy mark-on-push when false) |
| `-scrub-invalid-payloads-startup` | `true` | Remove queued records with invalid `payload_b64` during startup scrub |
| `-gossipv1-debug` | `false` | Enable verbose structured Gossip V1 debug logs |

### Federation Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-relay-id` | (auto-generated) | Unique relay ID |
| `-peer` | (empty) | Comma-separated list of peer relay WebSocket URLs |
| `-max-federation-conns` | `100` | Maximum concurrent inbound federation connections |
| `-envelope-store-path` | `store-path + .envelopes` | Path to envelope store database |
| `-max-envelope-size` | `65536` | Maximum envelope payload size in bytes |
| `-max-federation-peers` | `50` | Maximum number of federation peers |
| `-rate-limit-per-peer` | `100` | Rate limit per peer (requests per minute) |

### Relay Discovery Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-auto-peer-discovery` | `false` | Enable automatic peer discovery via gossip |
| `-descriptor-store-path` | `store-path + .descriptors` | Path to descriptor bbolt database |

### TAR (Traffic Analysis Resistance) Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-federation-topk` | `2` | Forwarding policy top-k candidate count (currently not active in relay-to-relay path) |
| `-federation-explore-prob` | `0.1` | Forwarding policy exploration probability (currently not active in relay-to-relay path) |
| `-federation-batch-interval` | `500ms` | TAR batch interval (currently not active in relay-to-relay path) |
| `-federation-batch-jitter` | `250ms` | TAR batch jitter range (currently not active in relay-to-relay path) |
| `-federation-batch-max` | `10` | TAR max frames per batch (currently not active in relay-to-relay path) |
| `-federation-pad-buckets` | `1024,4096,16384,65536` | TAR padding bucket sizes (currently not active in relay-to-relay path) |
| `-federation-pad-enabled` | `false` | Enable TAR payload padding (currently not active in relay-to-relay path) |
| `-federation-cover-enabled` | `false` | Enable TAR cover frames (currently not active in relay-to-relay path) |
| `-federation-cover-max` | `3` | TAR max cover frames when queue empty (currently not active in relay-to-relay path) |

Example of future TAR configuration (flag surface only; not active in current relay-to-relay scheduling path):

```bash
go run ./cmd/relay/main.go \
  -relay-id "relay-us-east" \
  -federation-topk 2 \
  -federation-explore-prob 0.1 \
  -federation-batch-interval 500ms \
  -federation-batch-jitter 250ms \
  -federation-pad-enabled true \
  -federation-pad-buckets "1024,4096,16384,65536" \
  -federation-cover-enabled true
```

## Startup Inventory Logs

On startup, relay logs an inventory summary to make persisted state visible:

- `queue_recipients`: recipients currently represented in queue indexes
- `queued_items`: unique queued message IDs currently tracked
- `envelopes`: non-expired envelope IDs in envelope store

Example:

```text
store: startup inventory summary queue_recipients=4 queued_items=69 envelopes=2
```

This is informational for operational visibility after restart and helps explain request decisions like `want_decision reason=already_have`.

## TTL Behavior

Messages and envelopes have time-to-live (TTL) that controls how long they persist:

The canonical specification for these behaviors is defined in the aethos protocol repository.
See [aethos/docs/spec](https://github.com/natemellendorf/aethos/tree/main/docs/spec).

- **Maximum TTL**: 7 days (604800 seconds) by default
- **Sweeper**: Runs every 30 seconds (configurable) to remove expired messages
- **Per-message TTL**: Clients can specify shorter TTL via `ttl_seconds` in send frame
- **TTL enforcement**: Expired messages are automatically removed on read and via periodic GC sweep

### Envelope TTL (Federation)

When messages are forwarded between relays:
- TTL is never extended - the original expiration is preserved
- Hop count increments with each forward (max 5 hops)
- Envelopes exceeding hop limit are dropped

## Health Endpoints

### GET /healthz

Health check - returns 200 if the process is up and store is open.

```bash
curl http://localhost:8081/healthz
# Response: ok
```

### GET /readyz

Readiness check - returns 200 if the store is initialized and ready to accept traffic.

```bash
curl http://localhost:8081/readyz
# Response: ready
```

### GET /metrics

Prometheus metrics endpoint.

```bash
curl http://localhost:8081/metrics
```

Available metrics include:
- `messages_received_total` - Total messages received
- `messages_persisted_total` - Total messages persisted
- `messages_delivered_total` - Total messages delivered
- `messages_expired_total` - Total messages expired
- `envelopes_received_total` - Total federation envelopes received
- `envelopes_forwarded_total` - Total federation envelopes forwarded
- `envelopes_dropped_total` - Total federation envelopes dropped
- `federation_peers_connected` - Current connected federation peers
- `federation_peer_score{peer="..."}` - Current score for a federation peer
- `federation_peer_acks_total{peer="..."}` - Total acks received from a peer
- `federation_peer_timeouts_total{peer="..."}` - Total timeouts for a peer

## Peer Scoring

The relay implements a peer scoring system to select optimal forwarding candidates:

### Score Components

- **Success Rate (35%)**: Ratio of successful acks to total attempts
- **Latency (25%)**: Exponential weighted moving average of ack latency
- **Stability (20%)**: Bonus for consecutive successes, penalty for consecutive failures
- **Uptime (20%)**: Connection uptime ratio

### Score Limits

The score is a **local heuristic only** - it reflects this relay's direct experience with each peer and is not a global reputation. Scores range from 0.0 to 1.0:

- 0.3+ is considered healthy
- Scores decay over time if no successful acks are received
- Consecutive failures trigger exponential backoff

### GET /federation/peers

View current peer metrics and scores:

```bash
curl http://localhost:8081/federation/peers
```

Response:
```json
{
  "peers": [
    {
      "peer_id": "peer-abc",
      "connected": true,
      "score": 0.85,
      "is_healthy": true,
      "acks_total": 150,
      "timeouts_total": 5,
      "ack_latency_ewma": 120.5
    }
  ],
  "count": 1
}
```

## Federation Protocol

Relays communicate via WebSocket at `/federation/ws` using **Gossip V1 binary envelopes**.

Authoritative relay-to-relay profile in this repo:

- [docs/federation/GOSSIPV1_RELAY_PROFILE.md](docs/federation/GOSSIPV1_RELAY_PROFILE.md)
- [docs/federation/CONFORMANCE_VECTORS.md](docs/federation/CONFORMANCE_VECTORS.md)

High-level flow:

- HELLO handshake
- SUMMARY inventory hint
- REQUEST missing item IDs
- TRANSFER objects (content-addressed)
- RECEIPT accepted item IDs

Legacy JSON relay federation frames (`relay_forward`, `relay_ack`) are deprecated and non-authoritative.

## Traffic Analysis Resistance (TAR)

The relay exposes TAR-related runtime configuration surfaces.

Current status in this repo:

- TAR/forwarding knobs are parsed and validated.
- They are not yet wired into active relay-to-relay Gossip V1 scheduling.
- Treat them as intra-domain policy surfaces, not inter-domain contract behavior.

### Batching and jitter (config surface)

When TAR scheduling is wired into the relay-to-relay path, outbound frames are expected to be batched and sent at intervals with random jitter:
- **Batching**: Frames are collected and sent in batches (up to `federation-batch-max` per tick)
- **Jitter**: Each tick interval includes random jitter (`+/-federation-batch-jitter`)

When active, this would make it harder for observers to correlate message sends with network timing.

### Padding (config surface)

When TAR padding is wired and enabled, payloads are expected to be padded to fixed-size buckets:
- Payloads are padded to the smallest bucket >= actual size
- Original content is preserved at the start; remainder is random bytes
- Example: 2000-byte payload becomes 4096 bytes

**Tradeoffs:**
- Increases bandwidth usage
- Hides actual message size from network observers
- Buckets should be chosen based on expected message size distribution

### Cover frames (config surface)

When TAR cover traffic is wired and enabled, and the send queue is empty, the relay is expected to send dummy "cover" frames:
- Frame type: `relay_cover` with timestamp and nonce
- Relay-to-relay only; not stored or forwarded beyond one hop

**Tradeoffs:**
- Creates constant traffic even when no messages to send
- Increases bandwidth but hides idle vs. active states
- Max cover frames per tick controls maximum overhead

## Security

### Origin Policy

In production, configure allowed origins to prevent cross-site WebSocket hijacking:

```bash
./relay -allowed-origins "https://app.aethos.io,https://aethos.app"
```

For local development, use dev mode:

```bash
./relay -dev-mode
```

**Warning:** Never use `-dev-mode` in production.

## Protocol

The relay uses a simple JSON-based WebSocket protocol:

The canonical specification for these behaviors is defined in the aethos protocol repository.
See [CLIENT_RELAY_PROTOCOL_V1.md](https://github.com/natemellendorf/aethos/blob/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md).

### Client → Server

**Hello**
```json
{"type": "hello", "wayfarer_id": "user123"}
```

**Send Message**
```json
{"type": "send", "to": "user456", "payload_b64": "SGVsbG8gV29ybGQ=", "ttl_seconds": 3600}
```

**Acknowledge Message**
```json
{"type": "ack", "msg_id": "msg-uuid"}
```

**Pull Queued Messages**
```json
{"type": "pull", "limit": 50}
```

### Server → Client

**Hello OK**
```json
{"type": "hello_ok"}
```

**Send OK**
```json
{"type": "send_ok", "msg_id": "msg-uuid", "at": 1234567890}
```

**Incoming Message**
```json
{"type": "message", "msg_id": "msg-uuid", "from": "user123", "payload_b64": "SGVsbG8gV29ybGQ=", "at": 1234567890}
```

**Messages (Pull Response)**
```json
{"type": "messages", "messages": [...]}
```

**Error**
```json
{"type": "error", "msg_id": "error description"}
```

## Per-Device Delivery

Messages are tracked per-device/wallet. When a message is delivered to one device, it is not suppressed for other devices of the same wayfarer ID. Each device must independently acknowledge messages to mark them as delivered.

The canonical specification for these behaviors is defined in the aethos protocol repository.
See [CLIENT_RELAY_PROTOCOL_V1.md](https://github.com/natemellendorf/aethos/blob/main/docs/spec/CLIENT_RELAY_PROTOCOL_V1.md).

This ensures:
- Multi-device users receive messages on all connected devices
- Offline devices don't block message delivery to online devices
- Each device has its own delivery state
