package model

const (
	// WebSocketReadLimitBytes caps inbound WebSocket frame size across client and federation paths.
	WebSocketReadLimitBytes = 2 * 1024 * 1024
)
