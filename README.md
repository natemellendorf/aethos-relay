# aethos-relay

WebSocket relay server with durable TTL persistence via bbolt.

## Features

- **WebSocket Server**: Real-time message delivery to connected clients
- **Durable Storage**: bbolt (embedded KV store) for message persistence
- **TTL Support**: Messages expire after configurable TTL
- **Background Sweeper**: Automatic cleanup of expired messages
- **Health & Metrics**: HTTP endpoints for health checks and Prometheus metrics
- **Zero Dependencies**: Single Go binary, no CGO, no external database

## Requirements

- Go 1.21+
- No SQLite
- No CGO required

## Quick Start

### Build

```bash
go build -o relay ./cmd/relay
```

### Run

```bash
./relay \
  --ws-addr :8080 \
  --http-addr :8081 \
  --store-path ./relay.db \
  --sweep-interval 30s \
  --max-ttl-seconds 604800
```

### Docker

```bash
docker build -t aethos-relay .
docker run -p 8080:8080 -p 8081:8081 aethos-relay
```

## Protocol

### Connection Flow

1. Connect to WebSocket endpoint: `ws://localhost:8080/ws`
2. Send hello frame with your wayfarer_id:
   ```json
   {"type":"hello","wayfarer_id":"user-123"}
   ```
3. Server responds:
   ```json
   {"type":"hello_ok"}
   ```

### Send Message

To send a message to another user:
```json
{
  "type": "send",
  "to": "user-456",
  "ttl_seconds": 3600,
  "payload_b64": "SGVsbG8gV29ybGQ="
}
```

Server responds:
```json
{
  "type": "send_ok",
  "msg_id": "uuid-here",
  "at": 1234567890
}
```

### Receive Messages

When a message arrives, server pushes:
```json
{
  "type": "message",
  "msg_id": "uuid-here",
  "from": "user-123",
  "payload_b64": "SGVsbG8gV29ybGQ=",
  "at": 1234567890
}
```

### Acknowledge Delivery

To acknowledge message receipt (removes from queue):
```json
{
  "type": "ack",
  "msg_id": "uuid-here"
}
```

### Pull Queued Messages

To fetch queued messages (when recipient was offline):
```json
{
  "type": "pull",
  "limit": 50
}
```

Server responds with message list:
```json
{
  "type": "messages",
  "messages": [...]
}
```

## Test Client Example

Using websocat (https://github.com/vi/websocat):

```bash
# Terminal 1 - Start relay
./relay --ws-addr :8080 --http-addr :8081

# Terminal 2 - Connect as user-a
websocat ws://localhost:8080/ws

# Send hello
{"type":"hello","wayfarer_id":"user-a"}

# Send message to user-b
{"type":"send","to":"user-b","ttl_seconds":3600,"payload_b64":"SGVsbG8g"}

# Terminal 3 - Connect as user-b (will receive message)
websocat ws://localhost:8080/ws
{"type":"hello","wayfarer_id":"user-b"}
```

### Go Test Client

```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	// Connect to relay
	u := "ws://localhost:8080/ws"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		fmt.Printf("dial: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Send hello
	hello := map[string]string{"type": "hello", "wayfarer_id": "test-client"}
	if err := c.WriteJSON(hello); err != nil {
		fmt.Printf("write hello: %v\n", err)
		os.Exit(1)
	}

	// Read hello_ok
	_, msg, err := c.ReadMessage()
	if err != nil {
		fmt.Printf("read: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Received: %s\n", string(msg))

	// Send a message
	send := map[string]interface{}{
		"type":        "send",
		"to":          "recipient-id",
		"ttl_seconds": 3600,
		"payload_b64": "SGVsbG8gV29ybGQ=",
	}
	if err := c.WriteJSON(send); err != nil {
		fmt.Printf("write send: %v\n", os.Exit(1)
	}

	// Wait for response
	_, msg, err = c.ReadMessage()
	if err != nil {
		fmt.Printf("read response: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Send response: %s\n", string(msg))

	// Check health
	resp, err := http.Get("http://localhost:8081/healthz")
	if err != nil {
		fmt.Printf("health check: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	fmt.Printf("Health: %s\n", resp.Status)
}
```

## HTTP Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | Health check (200 = healthy, 503 = unhealthy) |
| `GET /metrics` | Prometheus metrics |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `connections_current` | gauge | Current number of WebSocket connections |
| `messages_received_total` | counter | Total messages received |
| `messages_persisted_total` | counter | Total messages persisted to store |
| `messages_delivered_total` | counter | Total messages delivered to recipients |
| `messages_expired_total` | counter | Total messages expired and removed |
| `store_errors_total` | counter | Total store errors |

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--ws-addr` | `:8080` | WebSocket server address |
| `--http-addr` | `:8081` | HTTP server address for health/metrics |
| `--store-path` | `./relay.db` | Path to bbolt database file |
| `--sweep-interval` | `30s` | Interval for TTL sweeper |
| `--max-ttl-seconds` | `604800` | Maximum TTL (7 days) |
| `--log-json` | `false` | JSON logging output |

## Architecture

```
cmd/relay/main.go       - Entry point
internal/
  api/
    http.go            - HTTP handlers (health, metrics)
    ws.go              - WebSocket handlers
  model/
    message.go         - Message types
  store/
    bbolt_store.go    - bbolt implementation
    store.go          - Store interface
    ttl_sweeper.go    - TTL cleanup
    codec.go          - Message encoding
  metrics/
    metrics.go        - Prometheus metrics
```

## Development

```bash
# Run tests
go test ./...

# Run with race detector
go test -race ./...

# Vet
go vet ./...

# Format
gofmt -w .
```

## License

MIT
