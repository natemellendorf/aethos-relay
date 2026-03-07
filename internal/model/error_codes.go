package model

// ErrorCode is the canonical protocol vocabulary for error frame codes.
type ErrorCode string

const (
	ErrorCodeInvalidWayfarerID   ErrorCode = "INVALID_WAYFARER_ID"
	ErrorCodeToMismatch          ErrorCode = "TO_MISMATCH"
	ErrorCodeInvalidPayload      ErrorCode = "INVALID_PAYLOAD"
	ErrorCodePayloadTooLarge     ErrorCode = "PAYLOAD_TOO_LARGE"
	ErrorCodeRecipientOffline    ErrorCode = "RECIPIENT_OFFLINE"
	ErrorCodeRateLimited         ErrorCode = "RATE_LIMITED"
	ErrorCodeAuthFailed          ErrorCode = "AUTH_FAILED"
	ErrorCodeIdempotencyMismatch ErrorCode = "IDEMPOTENCY_MISMATCH"
	ErrorCodeInternalError       ErrorCode = "INTERNAL_ERROR"
)
