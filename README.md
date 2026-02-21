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

- **Multi-relay forwarding**: Messages are forwarded to all healthy peers except origin
- **Loop prevention**: Seen bucket tracks which relays have processed each envelope
- **Graceful degradation**: Unhealthy peers are skipped; messages still reach healthy peers
- **Backoff**: Failed peers have exponential backoff before retry

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
