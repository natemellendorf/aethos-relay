package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/xerrors"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

var (
	// Bucket names
	BucketMessages         = []byte("messages")
	BucketQueueByRecipient = []byte("queue_by_recipient")
	BucketDeliveryState    = []byte("delivery_state")
	BucketExpiryIndex      = []byte("expiry_index")
	BucketMsgIDIndex       = []byte("msgid_index")
	BucketMeta             = []byte("meta")

	// Meta keys
	MetaLastSweep = []byte("last_sweep")

	ErrInvalidKey = errors.New("invalid key format")
)

// BBoltStore implements Store using bbolt.
type BBoltStore struct {
	path string
	db   *bolt.DB
}

// NewBBoltStore creates a new BBoltStore.
func NewBBoltStore(path string) *BBoltStore {
	return &BBoltStore{path: path}
}

// Open opens the bbolt database.
func (s *BBoltStore) Open() error {
	db, err := bolt.Open(s.path, 0600, nil)
	if err != nil {
		return xerrors.Errorf("failed to open bbolt store: %w", err)
	}
	s.db = db

	// Create buckets if they don't exist
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{
			BucketMessages,
			BucketQueueByRecipient,
			BucketDeliveryState,
			BucketExpiryIndex,
			BucketMsgIDIndex,
			BucketMeta,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return xerrors.Errorf("failed to create bucket %s: %w", string(b), err)
			}
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to initialize buckets: %w", err)
	}

	return nil
}

// Close closes the bbolt database.
func (s *BBoltStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// PersistMessage stores a message and adds it to the recipient's queue atomically.
func (s *BBoltStore) PersistMessage(ctx context.Context, msg *model.Message) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Store the message
		msgBytes, err := EncodeMessage(msg)
		if err != nil {
			return xerrors.Errorf("failed to encode message: %w", err)
		}
		if err := tx.Bucket(BucketMessages).Put([]byte(msg.ID), msgBytes); err != nil {
			return xerrors.Errorf("failed to store message: %w", err)
		}

		// Add to queue by recipient
		queueKey := EncodeQueueKey(&model.QueueKey{
			To:        msg.To,
			CreatedAt: msg.CreatedAt,
			MsgID:     msg.ID,
		})
		if err := tx.Bucket(BucketQueueByRecipient).Put(queueKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add to queue: %w", err)
		}

		// Add to expiry index
		expiryKey := EncodeExpiryKey(msg.ID, msg.ExpiresAt)
		if err := tx.Bucket(BucketExpiryIndex).Put(expiryKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add to expiry index: %w", err)
		}

		// Add msgid index for quick lookup
		msgIDKey := EncodeMsgIDKey(msg.ID)
		if err := tx.Bucket(BucketMsgIDIndex).Put(msgIDKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add msgid index: %w", err)
		}

		return nil
	})
}

// GetQueuedMessages retrieves messages for a recipient that haven't been delivered to this specific wayfarer.
func (s *BBoltStore) GetQueuedMessages(ctx context.Context, recipientID string, limit int) ([]*model.Message, error) {
	var messages []*model.Message

	err := s.db.View(func(tx *bolt.Tx) error {
		// Prefix scan the queue bucket
		c := tx.Bucket(BucketQueueByRecipient).Cursor()
		prefix := []byte(recipientID + string(rune(0)))

		count := 0
		for k, v := c.Seek(prefix); k != nil && count < limit; k, v = c.Next() {
			// Check if we've moved past our prefix
			if len(k) < len(prefix) {
				break
			}
			if string(k[:len(prefix)]) != string(prefix) {
				break
			}

			msgID := string(v)
			msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
			if msgBytes == nil {
				continue
			}

			msg, err := DecodeMessage(msgBytes)
			if err != nil {
				continue // Skip malformed messages
			}

			// Check per-device delivery state - only return if not delivered to this specific recipient
			deliveryKey := EncodeDeliveryKey(msgID, recipientID)
			delivered := tx.Bucket(BucketDeliveryState).Get(deliveryKey)
			if delivered == nil {
				messages = append(messages, msg)
			}
			count++
		}
		return nil
	})

	return messages, err
}

// MarkDelivered marks a message as delivered to a specific recipient.
// This enables per-device delivery tracking.
func (s *BBoltStore) MarkDelivered(ctx context.Context, msgID string, recipientID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
		if msgBytes == nil {
			return fmt.Errorf("message not found: %s", msgID)
		}

		msg, err := DecodeMessage(msgBytes)
		if err != nil {
			return xerrors.Errorf("failed to decode message: %w", err)
		}

		// Store per-device delivery state with composite key: msgID|recipientID
		deliveryKey := EncodeDeliveryKey(msgID, recipientID)
		now := time.Now()
		nowBytes, _ := now.MarshalBinary()
		if err := tx.Bucket(BucketDeliveryState).Put(deliveryKey, nowBytes); err != nil {
			return xerrors.Errorf("failed to update delivery state: %w", err)
		}

		// Update message DeliveredAt if this is the first delivery
		if msg.DeliveredAt == nil {
			msg.DeliveredAt = &now
			updatedBytes, err := EncodeMessage(msg)
			if err != nil {
				return xerrors.Errorf("failed to encode message: %w", err)
			}
			if err := tx.Bucket(BucketMessages).Put([]byte(msgID), updatedBytes); err != nil {
				return xerrors.Errorf("failed to update message: %w", err)
			}
		}

		return nil
	})
}

// IsDeliveredTo checks if a message has been delivered to a specific recipient.
func (s *BBoltStore) IsDeliveredTo(ctx context.Context, msgID string, recipientID string) (bool, error) {
	var delivered bool
	err := s.db.View(func(tx *bolt.Tx) error {
		deliveryKey := EncodeDeliveryKey(msgID, recipientID)
		result := tx.Bucket(BucketDeliveryState).Get(deliveryKey)
		delivered = result != nil
		return nil
	})
	return delivered, err
}

// RemoveMessage removes a message from all buckets.
func (s *BBoltStore) RemoveMessage(ctx context.Context, msgID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Get message first to find queue key
		msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
		if msgBytes != nil {
			msg, err := DecodeMessage(msgBytes)
			if err == nil {
				// Remove from queue
				queueKey := EncodeQueueKey(&model.QueueKey{
					To:        msg.To,
					CreatedAt: msg.CreatedAt,
					MsgID:     msg.ID,
				})
				tx.Bucket(BucketQueueByRecipient).Delete(queueKey)

				// Remove from expiry index
				expiryKey := EncodeExpiryKey(msg.ID, msg.ExpiresAt)
				tx.Bucket(BucketExpiryIndex).Delete(expiryKey)
			}
		}

		// Remove from messages
		tx.Bucket(BucketMessages).Delete([]byte(msgID))

		// Remove from msgid index
		tx.Bucket(BucketMsgIDIndex).Delete([]byte(msgID))

		// Remove from delivery state
		tx.Bucket(BucketDeliveryState).Delete([]byte(msgID))

		return nil
	})
}

// GetExpiredMessages returns messages that have expired.
func (s *BBoltStore) GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error) {
	var messages []*model.Message
	beforeStr := before.Format(time.RFC3339Nano)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketExpiryIndex).Cursor()

		// Iterate through all keys and check if expired
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Parse the key to extract expires_at
			parts := bytes.Split(k, []byte{0})
			if len(parts) < 2 {
				continue
			}
			expiresAtStr := string(parts[0])
			if expiresAtStr > beforeStr {
				break // Not yet expired, and keys are sorted so we can break
			}

			msgID := string(v)
			msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
			if msgBytes == nil {
				continue
			}

			msg, err := DecodeMessage(msgBytes)
			if err != nil {
				continue
			}

			messages = append(messages, msg)
		}
		return nil
	})

	return messages, err
}

// GetLastSweepTime returns the last time the sweeper ran.
func (s *BBoltStore) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(BucketMeta).Get(MetaLastSweep)
		if v == nil {
			t = time.Time{} // Zero time means never
			return nil
		}
		// Parse as Unix timestamp for simplicity
		var unix int64
		if len(v) >= 8 {
			unix = int64(v[0])<<56 | int64(v[1])<<48 | int64(v[2])<<40 | int64(v[3])<<32 |
				int64(v[4])<<24 | int64(v[5])<<16 | int64(v[6])<<8 | int64(v[7])
		}
		t = time.Unix(unix, 0)
		return nil
	})
	return t, err
}

// SetLastSweepTime records the last sweeper run time.
func (s *BBoltStore) SetLastSweepTime(ctx context.Context, t time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		unix := t.Unix()
		v := []byte{
			byte(unix >> 56),
			byte(unix >> 48),
			byte(unix >> 40),
			byte(unix >> 32),
			byte(unix >> 24),
			byte(unix >> 16),
			byte(unix >> 8),
			byte(unix),
		}
		return tx.Bucket(BucketMeta).Put(MetaLastSweep, v)
	})
}
