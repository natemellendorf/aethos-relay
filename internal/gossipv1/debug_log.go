package gossipv1

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
)

const defaultDebugItemSampleLimit = 4

var (
	debugLoggingEnabled atomic.Bool
	debugSessionCounter atomic.Uint64
)

type debugTraceContextKey struct{}

// DebugTrace carries session metadata for deep per-item debug logs.
type DebugTrace struct {
	SessionID string
	Peer      string
}

// SetDebugLoggingEnabled toggles verbose Gossip V1 debug logging.
func SetDebugLoggingEnabled(enabled bool) {
	debugLoggingEnabled.Store(enabled)
}

// DebugLoggingEnabled reports whether verbose Gossip V1 debug logging is enabled.
func DebugLoggingEnabled() bool {
	return debugLoggingEnabled.Load()
}

// NewDebugSessionID returns a monotonic session identifier.
func NewDebugSessionID(prefix string) string {
	if prefix == "" {
		prefix = "gossipv1"
	}
	return fmt.Sprintf("%s-%06d", prefix, debugSessionCounter.Add(1))
}

// WithDebugTrace attaches session metadata for downstream debug logs.
func WithDebugTrace(ctx context.Context, sessionID string, peer string) context.Context {
	return context.WithValue(ctx, debugTraceContextKey{}, DebugTrace{SessionID: sessionID, Peer: peer})
}

// DebugTraceFromContext extracts session metadata for downstream debug logs.
func DebugTraceFromContext(ctx context.Context) (DebugTrace, bool) {
	if ctx == nil {
		return DebugTrace{}, false
	}
	trace, ok := ctx.Value(debugTraceContextKey{}).(DebugTrace)
	if !ok {
		return DebugTrace{}, false
	}
	return trace, true
}

// DebugLoggerFromContext builds a logger from context-bound trace metadata.
func DebugLoggerFromContext(ctx context.Context, fallbackPeer string) DebugSessionLogger {
	trace, ok := DebugTraceFromContext(ctx)
	if !ok {
		return NewDebugSessionLogger("gossipv1-store", fallbackPeer)
	}
	sessionID := trace.SessionID
	if sessionID == "" {
		sessionID = "gossipv1-store"
	}
	peer := trace.Peer
	if peer == "" {
		peer = fallbackPeer
	}
	return NewDebugSessionLogger(sessionID, peer)
}

// DebugSessionLogger emits key=val Gossip V1 debug logs with common fields.
type DebugSessionLogger struct {
	SessionID string
	Peer      string
}

// NewDebugSessionLogger creates a debug logger bound to session and peer identity.
func NewDebugSessionLogger(sessionID string, peer string) DebugSessionLogger {
	return DebugSessionLogger{SessionID: sessionID, Peer: peer}
}

// WithPeer returns a copy bound to a new peer identity.
func (l DebugSessionLogger) WithPeer(peer string) DebugSessionLogger {
	l.Peer = peer
	return l
}

// Log emits a structured key=val debug event.
func (l DebugSessionLogger) Log(direction string, msgType string, event string, kv ...any) {
	if !DebugLoggingEnabled() {
		return
	}
	if direction == "" {
		direction = "local"
	}
	if msgType == "" {
		msgType = "UNKNOWN"
	}
	if event == "" {
		event = "unspecified"
	}
	sessionID := l.SessionID
	if sessionID == "" {
		sessionID = "gossipv1-unknown"
	}
	peer := l.Peer
	if peer == "" {
		peer = "-"
	}

	var builder strings.Builder
	builder.WriteString("gossipv1_debug")
	appendDebugKV(&builder, "event", event)
	appendDebugKV(&builder, "session_id", sessionID)
	appendDebugKV(&builder, "peer", peer)
	appendDebugKV(&builder, "direction", direction)
	appendDebugKV(&builder, "msg_type", msgType)

	for idx := 0; idx+1 < len(kv); idx += 2 {
		key, ok := kv[idx].(string)
		if !ok || key == "" {
			key = fmt.Sprintf("arg_%d", idx)
		}
		appendDebugKV(&builder, key, kv[idx+1])
	}
	if len(kv)%2 != 0 {
		appendDebugKV(&builder, "kv_error", "odd_pairs")
	}

	log.Print(builder.String())
}

// LogItem emits a structured key=val debug event that includes item_id.
func (l DebugSessionLogger) LogItem(direction string, msgType string, itemID string, event string, kv ...any) {
	values := make([]any, 0, len(kv)+2)
	values = append(values, "item_id", itemID)
	values = append(values, kv...)
	l.Log(direction, msgType, event, values...)
}

// ItemIDSample returns count, first-N sample, and a short digest of the full set.
func ItemIDSample(ids []string, sampleLimit int) (int, string, string) {
	if sampleLimit <= 0 {
		sampleLimit = defaultDebugItemSampleLimit
	}
	count := len(ids)
	if count == 0 {
		return 0, "", ""
	}

	sampleCount := min(sampleLimit, count)
	sampleValues := make([]string, 0, sampleCount)
	for _, id := range ids[:sampleCount] {
		sampleValues = append(sampleValues, id)
	}

	hasher := sha256.New()
	for _, id := range ids {
		hasher.Write([]byte(id))
		hasher.Write([]byte{0})
	}
	digest := hasher.Sum(nil)
	return count, strings.Join(sampleValues, ","), hex.EncodeToString(digest[:6])
}

// FrameTypeFromPrefixedBinaryFrame extracts the gossip frame type from a length-prefixed binary frame.
func FrameTypeFromPrefixedBinaryFrame(data []byte) string {
	if len(data) < 5 {
		return "UNKNOWN"
	}

	frameLen := binary.BigEndian.Uint32(data[:4])
	if frameLen == 0 || frameLen > MaxFrameBytes {
		return "UNKNOWN"
	}
	if int(frameLen) != len(data[4:]) {
		return "UNKNOWN"
	}

	envelope, err := DecodeEnvelope(data[4:])
	if err != nil {
		return "UNKNOWN"
	}
	if envelope.Type == "" {
		return "UNKNOWN"
	}

	return envelope.Type
}

func appendDebugKV(builder *strings.Builder, key string, value any) {
	builder.WriteByte(' ')
	builder.WriteString(key)
	builder.WriteByte('=')
	builder.WriteString(formatDebugValue(value))
}

func formatDebugValue(value any) string {
	if value == nil {
		return "nil"
	}

	var rendered string
	switch typed := value.(type) {
	case error:
		rendered = typed.Error()
	case string:
		rendered = typed
	default:
		rendered = fmt.Sprint(value)
	}

	if rendered == "" {
		return "\"\""
	}
	if strings.ContainsAny(rendered, " \t\n\r=\"") {
		return strconv.Quote(rendered)
	}
	return rendered
}
