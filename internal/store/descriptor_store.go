package store

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/xerrors"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// Bucket names for descriptors
var (
	BucketDescriptors   = []byte("descriptors")
	BucketDescriptorIdx = []byte("descriptor_expiry_index")
)

// DescriptorStore adds descriptor persistence to the store.
type DescriptorStore interface {
	Store
	// PutDescriptor stores or updates a descriptor.
	PutDescriptor(ctx context.Context, d *model.RelayDescriptor) error
	// GetDescriptor retrieves a descriptor by relay_id.
	GetDescriptor(ctx context.Context, relayID string) (*model.RelayDescriptor, error)
	// GetAllDescriptors returns all descriptors.
	GetAllDescriptors(ctx context.Context) ([]*model.RelayDescriptor, error)
	// GetExpiredDescriptors returns descriptors that have expired.
	GetExpiredDescriptors(ctx context.Context, before time.Time) ([]*model.RelayDescriptor, error)
	// DeleteDescriptor removes a descriptor.
	DeleteDescriptor(ctx context.Context, relayID string) error
	// CountDescriptors returns the total number of descriptors.
	CountDescriptors(ctx context.Context) (int, error)
}

// BBoltDescriptorStore implements DescriptorStore using bbolt.
type BBoltDescriptorStore struct {
	path string
	db   *bolt.DB
}

// NewBBoltDescriptorStore creates a new BBoltDescriptorStore.
func NewBBoltDescriptorStore(path string) *BBoltDescriptorStore {
	return &BBoltDescriptorStore{path: path}
}

// Open opens the bbolt database.
func (s *BBoltDescriptorStore) Open() error {
	db, err := bolt.Open(s.path, 0600, nil)
	if err != nil {
		return xerrors.Errorf("failed to open bbolt store: %w", err)
	}
	s.db = db

	// Create buckets if they don't exist
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{
			BucketDescriptors,
			BucketDescriptorIdx,
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
func (s *BBoltDescriptorStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// PutDescriptor stores or updates a descriptor.
// If the descriptor already exists, it updates last_seen_at.
func (s *BBoltDescriptorStore) PutDescriptor(ctx context.Context, d *model.RelayDescriptor) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Check if descriptor exists
		existing := tx.Bucket(BucketDescriptors).Get([]byte(d.RelayID))
		now := time.Now()

		if existing != nil {
			// Update existing: only update last_seen_at, preserve first_seen_at
			var existingDesc model.RelayDescriptor
			if err := json.Unmarshal(existing, &existingDesc); err == nil {
				d.FirstSeenAt = existingDesc.FirstSeenAt
			}
		} else {
			// New descriptor: set first_seen_at
			d.FirstSeenAt = now.Unix()
		}

		d.LastSeenAt = now.Unix()

		// Encode and store
		data, err := json.Marshal(d)
		if err != nil {
			return xerrors.Errorf("failed to encode descriptor: %w", err)
		}

		if err := tx.Bucket(BucketDescriptors).Put([]byte(d.RelayID), data); err != nil {
			return xerrors.Errorf("failed to store descriptor: %w", err)
		}

		// Update expiry index
		expiryKey := EncodeDescriptorExpiryKey(d.RelayID, d.ExpiresAt)
		if err := tx.Bucket(BucketDescriptorIdx).Put(expiryKey, []byte(d.RelayID)); err != nil {
			return xerrors.Errorf("failed to update descriptor expiry index: %w", err)
		}

		return nil
	})
}

// GetDescriptor retrieves a descriptor by relay_id.
func (s *BBoltDescriptorStore) GetDescriptor(ctx context.Context, relayID string) (*model.RelayDescriptor, error) {
	var desc *model.RelayDescriptor

	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
		if data == nil {
			return nil // Not found, return nil
		}

		var d model.RelayDescriptor
		if err := json.Unmarshal(data, &d); err != nil {
			return xerrors.Errorf("failed to decode descriptor: %w", err)
		}
		desc = &d
		return nil
	})

	return desc, err
}

// GetAllDescriptors returns all descriptors.
func (s *BBoltDescriptorStore) GetAllDescriptors(ctx context.Context) ([]*model.RelayDescriptor, error) {
	var descriptors []*model.RelayDescriptor

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptors).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var d model.RelayDescriptor
			if err := json.Unmarshal(v, &d); err != nil {
				continue // Skip malformed
			}
			descriptors = append(descriptors, &d)
		}
		return nil
	})

	return descriptors, err
}

// GetExpiredDescriptors returns descriptors that have expired.
func (s *BBoltDescriptorStore) GetExpiredDescriptors(ctx context.Context, before time.Time) ([]*model.RelayDescriptor, error) {
	var descriptors []*model.RelayDescriptor
	beforeStr := before.Format(time.RFC3339Nano)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptorIdx).Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			parts := bytes.Split(k, []byte{0})
			if len(parts) < 2 {
				continue
			}

			expiresAtStr := string(parts[0])
			if expiresAtStr > beforeStr {
				break // Not yet expired
			}

			relayID := string(v)
			data := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
			if data == nil {
				continue
			}

			var d model.RelayDescriptor
			if err := json.Unmarshal(data, &d); err != nil {
				continue
			}
			descriptors = append(descriptors, &d)
		}
		return nil
	})

	return descriptors, err
}

// DeleteDescriptor removes a descriptor.
func (s *BBoltDescriptorStore) DeleteDescriptor(ctx context.Context, relayID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Get descriptor to find expiry key
		data := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
		if data != nil {
			var d model.RelayDescriptor
			if err := json.Unmarshal(data, &d); err == nil {
				expiryKey := EncodeDescriptorExpiryKey(d.RelayID, d.ExpiresAt)
				tx.Bucket(BucketDescriptorIdx).Delete(expiryKey)
			}
		}

		tx.Bucket(BucketDescriptors).Delete([]byte(relayID))
		return nil
	})
}

// CountDescriptors returns the total number of descriptors.
func (s *BBoltDescriptorStore) CountDescriptors(ctx context.Context) (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(BucketDescriptors).Stats().KeyN
		return nil
	})
	return count, err
}

// EncodeDescriptorExpiryKey encodes a descriptor expiry key.
func EncodeDescriptorExpiryKey(relayID string, expiresAt int64) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(time.Unix(expiresAt, 0).Format(time.RFC3339Nano))
	buf.WriteByte(0)
	buf.WriteString(relayID)
	return buf.Bytes()
}

// PersistMessage stores a message (for Store interface compatibility).
func (s *BBoltDescriptorStore) PersistMessage(ctx context.Context, msg *model.Message) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		msgBytes, err := EncodeMessage(msg)
		if err != nil {
			return xerrors.Errorf("failed to encode message: %w", err)
		}
		if err := tx.Bucket(BucketMessages).Put([]byte(msg.ID), msgBytes); err != nil {
			return xerrors.Errorf("failed to store message: %w", err)
		}

		queueKey := EncodeQueueKey(&model.QueueKey{
			To:        msg.To,
			CreatedAt: msg.CreatedAt,
			MsgID:     msg.ID,
		})
		if err := tx.Bucket(BucketQueueByRecipient).Put(queueKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add to queue: %w", err)
		}

		expiryKey := EncodeExpiryKey(msg.ID, msg.ExpiresAt)
		if err := tx.Bucket(BucketExpiryIndex).Put(expiryKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add to expiry index: %w", err)
		}

		msgIDKey := EncodeMsgIDKey(msg.ID)
		if err := tx.Bucket(BucketMsgIDIndex).Put(msgIDKey, []byte(msg.ID)); err != nil {
			return xerrors.Errorf("failed to add msgid index: %w", err)
		}

		return nil
	})
}

// GetQueuedMessages retrieves undelivered messages for a recipient.
func (s *BBoltDescriptorStore) GetQueuedMessages(ctx context.Context, to string, limit int) ([]*model.Message, error) {
	var messages []*model.Message

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketQueueByRecipient).Cursor()
		prefix := []byte(to + string(rune(0)))

		count := 0
		for k, v := c.Seek(prefix); k != nil && count < limit; k, v = c.Next() {
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
				continue
			}

			if !msg.Delivered {
				messages = append(messages, msg)
			}
			count++
		}
		return nil
	})

	return messages, err
}

// MarkDelivered marks a message as delivered.
func (s *BBoltDescriptorStore) MarkDelivered(ctx context.Context, msgID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
		if msgBytes == nil {
			return xerrors.Errorf("message not found: %s", msgID)
		}

		msg, err := DecodeMessage(msgBytes)
		if err != nil {
			return xerrors.Errorf("failed to decode message: %w", err)
		}

		msg.Delivered = true
		now := time.Now()
		msg.DeliveredAt = &now

		updatedBytes, err := EncodeMessage(msg)
		if err != nil {
			return xerrors.Errorf("failed to encode message: %w", err)
		}
		if err := tx.Bucket(BucketMessages).Put([]byte(msgID), updatedBytes); err != nil {
			return xerrors.Errorf("failed to update message: %w", err)
		}

		if err := tx.Bucket(BucketDeliveryState).Put([]byte(msgID), []byte("delivered")); err != nil {
			return xerrors.Errorf("failed to update delivery state: %w", err)
		}

		return nil
	})
}

// RemoveMessage removes a message from all buckets.
func (s *BBoltDescriptorStore) RemoveMessage(ctx context.Context, msgID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		msgBytes := tx.Bucket(BucketMessages).Get([]byte(msgID))
		if msgBytes != nil {
			msg, err := DecodeMessage(msgBytes)
			if err == nil {
				queueKey := EncodeQueueKey(&model.QueueKey{
					To:        msg.To,
					CreatedAt: msg.CreatedAt,
					MsgID:     msg.ID,
				})
				tx.Bucket(BucketQueueByRecipient).Delete(queueKey)

				expiryKey := EncodeExpiryKey(msg.ID, msg.ExpiresAt)
				tx.Bucket(BucketExpiryIndex).Delete(expiryKey)
			}
		}

		tx.Bucket(BucketMessages).Delete([]byte(msgID))
		tx.Bucket(BucketMsgIDIndex).Delete([]byte(msgID))
		tx.Bucket(BucketDeliveryState).Delete([]byte(msgID))

		return nil
	})
}

// GetExpiredMessages returns messages that have expired.
func (s *BBoltDescriptorStore) GetExpiredMessages(ctx context.Context, before time.Time) ([]*model.Message, error) {
	var messages []*model.Message
	beforeStr := before.Format(time.RFC3339Nano)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketExpiryIndex).Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			parts := bytes.Split(k, []byte{0})
			if len(parts) < 2 {
				continue
			}
			expiresAtStr := string(parts[0])
			if expiresAtStr > beforeStr {
				break
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
func (s *BBoltDescriptorStore) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(BucketMeta).Get(MetaLastSweep)
		if v == nil {
			t = time.Time{}
			return nil
		}
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
func (s *BBoltDescriptorStore) SetLastSweepTime(ctx context.Context, t time.Time) error {
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
