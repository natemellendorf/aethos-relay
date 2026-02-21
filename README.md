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
go run ./cmd/relay/main.go -http-addr :8081
```

This starts a relay on port 8081 with default settings.

### Run with Federation

Connect to peer relays for mesh networking:

```bash
go run ./cmd/relay/main.go \
  -relay-id "relay-us-east" \
  -peer "ws://relay-1.example.com:8081/federation/ws,ws://relay-2.example.com:8081/federation/ws" \
  -http-addr :8081
```

## Flags

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
| `-federation-topk` | `2` | Number of top peers to forward to |
| `-federation-explore-prob` | `0.1` | Probability of exploring a non-topK peer |
| `-federation-batch-interval` | `500ms` | Batching interval |
| `-federation-batch-jitter` | `250ms` | Batching jitter range |
| `-federation-batch-max` | `10` | Max frames per batch |
| `-federation-pad-buckets` | `1024,4096,16384,65536` | Padding bucket sizes (comma-separated) |
| `-federation-pad-enabled` | `false` | Enable payload padding |
| `-federation-cover-enabled` | `false` | Enable cover frames |
| `-federation-cover-max` | `3` | Max cover frames when queue empty |

Example with TAR enabled:

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

## TTL Behavior

Messages and envelopes have time-to-live (TTL) that controls how long they persist:

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

Relays communicate via WebSocket at `/federation/ws`:

### Frames

**relay_hello** - Initial handshake:
```json
{"type": "relay_hello", "relay_id": "relay-us-east", "version": "1.0"}
```

**relay_forward** - Forward envelope:
```json
{"type": "relay_forward", "envelope": {...}}
```

**relay_ack** - Acknowledge receipt:
```json
{"type": "relay_ack", "envelope_id": "...", "destination": "...", "status": "accepted"}
```

### Federation Behavior

- **Multi-relay forwarding**: Messages are forwarded to selected peers based on score (topK + exploration)
- **Loop prevention**: Seen bucket tracks which relays have processed each envelope
- **Graceful degradation**: Unhealthy peers are skipped; messages still reach healthy peers
- **Backoff**: Failed peers have exponential backoff before retry

## Traffic Analysis Resistance (TAR)

The relay implements several techniques to resist traffic analysis:

### Batching and Jitter

Outbound frames are batched and sent at intervals with random jitter:
- **Batching**: Frames are collected and sent in batches (up to `federation-batch-max` per tick)
- **Jitter**: Each tick interval includes random jitter (`+/-federation-batch-jitter`)

This makes it harder for observers to correlate message sends with network timing.

### Padding

When enabled, payloads are padded to fixed-size buckets:
- Payloads are padded to the smallest bucket >= actual size
- Original content is preserved at the start; remainder is random bytes
- Example: 2000-byte payload becomes 4096 bytes

**Tradeoffs:**
- Increases bandwidth usage
- Hides actual message size from network observers
- Buckets should be chosen based on expected message size distribution

### Cover Frames

When enabled and the send queue is empty, the relay sends dummy "cover" frames:
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

This ensures:
- Multi-device users receive messages on all connected devices
- Offline devices don't block message delivery to online devices
- Each device has its own delivery state
