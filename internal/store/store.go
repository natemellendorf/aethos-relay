package store

import (
	"context"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// Store defines the interface for message persistence.
type Store interface {
	// Open opens the store.
	Open() error

	// Close closes the store.
	Close() error

	// PersistMessage stores a message and adds it to the recipient's queue.
	PersistMessage(ctx context.Context, msg *model.Message) error

	// GetQueuedMessages retrieves undelivered messages for a recipient.
	GetQueuedMessages(ctx context.Context, to string, limit int) ([]*model.Message, error)

	// MarkDelivered marks a message as delivered.
	MarkDelivered(ctx context.Context, msgID string) error

	// RemoveMessage removes a message from all buckets.
	RemoveMessage(ctx context.Context, msgID string) error

	// GetExpiredMessages returns messages that have expired.
	GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error)

	// GetLastSweepTime returns the last time the sweeper ran.
	GetLastSweepTime(ctx context.Context) (time.Time, error)

	// SetLastSweepTime records the last sweeper run time.
	SetLastSweepTime(ctx context.Context, t time.Time) error
}
