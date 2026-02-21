# aethos-relay

WebSocket relay server for Aethos with message queuing and delivery tracking.

## Features

- WebSocket-based real-time messaging
- Persistent message queue with TTL support
- Per-device delivery tracking (messages delivered to one device won't be suppressed for other devices of the same user)
- Configurable WebSocket origin policy for production security

## Usage

```bash
./relay [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-ws-addr` | `:8080` | WebSocket server address |
| `-http-addr` | `:8081` | HTTP server address |
| `-store-path` | `./relay.db` | Path to bbolt database |
| `-sweep-interval` | `30s` | TTL sweeper interval |
| `-max-ttl-seconds` | `604800` | Maximum TTL in seconds (default 7 days) |
| `-log-json` | `false` | JSON logging output |
| `-allowed-origins` | (empty) | Comma-separated list of allowed WebSocket origins (e.g., `https://app.aethos.io,https://aethos.app`) |
| `-dev-mode` | `false` | Enable development mode (allows all origins, for local development only) |

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
