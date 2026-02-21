package store

import (
	"bytes"
	"context"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/xerrors"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

// BBoltEnvelopeStore implements EnvelopeStore using bbolt.
type BBoltEnvelopeStore struct {
	path  string
	db    *bolt.DB
	codec model.EnvelopeCodec
}

// NewBBoltEnvelopeStore creates a new BBoltEnvelopeStore.
func NewBBoltEnvelopeStore(path string) *BBoltEnvelopeStore {
	return &BBoltEnvelopeStore{path: path}
}

// Open opens the bbolt database for envelopes.
func (s *BBoltEnvelopeStore) Open() error {
	db, err := bolt.Open(s.path, 0600, nil)
	if err != nil {
		return xerrors.Errorf("failed to open envelope store: %w", err)
	}
	s.db = db

	// Create buckets if they don't exist
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{
			model.BucketEnvelopes,
			model.BucketByDestination,
			model.BucketByExpiry,
			model.BucketSeen,
			model.BucketMeta,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return xerrors.Errorf("failed to create bucket %s: %w", string(b), err)
			}
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to initialize envelope buckets: %w", err)
	}

	return nil
}

// Close closes the bbolt database.
func (s *BBoltEnvelopeStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// PersistEnvelope stores an envelope and adds it to destination and expiry indexes.
func (s *BBoltEnvelopeStore) PersistEnvelope(ctx context.Context, env *model.Envelope) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Validate envelope before persisting
		if err := env.Validate(); err != nil {
			return xerrors.Errorf("invalid envelope: %w", err)
		}

		// Store the envelope
		envBytes, err := s.codec.EncodeEnvelope(env)
		if err != nil {
			return xerrors.Errorf("failed to encode envelope: %w", err)
		}
		if err := tx.Bucket(model.BucketEnvelopes).Put([]byte(env.ID), envBytes); err != nil {
			return xerrors.Errorf("failed to store envelope: %w", err)
		}

		// Add to destination index
		destKey := model.DestinationIndexKey(env.DestinationID, env.ID)
		if err := tx.Bucket(model.BucketByDestination).Put(destKey, []byte(env.ID)); err != nil {
			return xerrors.Errorf("failed to add to destination index: %w", err)
		}

		// Add to expiry index
		expiryKey := model.ExpiryIndexKey(env.ExpiresAt, env.ID)
		if err := tx.Bucket(model.BucketByExpiry).Put(expiryKey, []byte(env.ID)); err != nil {
			return xerrors.Errorf("failed to add to expiry index: %w", err)
		}

		return nil
	})
}

// GetEnvelopeByID retrieves an envelope by its ID.
func (s *BBoltEnvelopeStore) GetEnvelopeByID(ctx context.Context, envID string) (*model.Envelope, error) {
	var env *model.Envelope
	err := s.db.View(func(tx *bolt.Tx) error {
		envBytes := tx.Bucket(model.BucketEnvelopes).Get([]byte(envID))
		if envBytes == nil {
			return fmt.Errorf("envelope not found: %s", envID)
		}
		var err error
		env, err = s.codec.DecodeEnvelope(envBytes)
		if err != nil {
			return xerrors.Errorf("failed to decode envelope: %w", err)
		}
		return nil
	})
	return env, err
}

// GetEnvelopesByDestination retrieves envelopes for a destination.
func (s *BBoltEnvelopeStore) GetEnvelopesByDestination(ctx context.Context, destID string, limit int) ([]*model.Envelope, error) {
	var envelopes []*model.Envelope

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(model.BucketByDestination).Cursor()
		prefix := []byte(destID + string(rune(0)))

		count := 0
		for k, v := c.Seek(prefix); k != nil && count < limit; k, v = c.Next() {
			// Check if we've moved past our prefix
			if len(k) < len(prefix) {
				break
			}
			if !bytes.HasPrefix(k, prefix) {
				break
			}

			envID := string(v)
			envBytes := tx.Bucket(model.BucketEnvelopes).Get([]byte(envID))
			if envBytes == nil {
				continue
			}

			env, err := s.codec.DecodeEnvelope(envBytes)
			if err != nil {
				continue // Skip malformed envelopes
			}

			// Check if expired
			if env.IsExpired() {
				continue
			}

			envelopes = append(envelopes, env)
			count++
		}
		return nil
	})

	return envelopes, err
}

// RemoveEnvelope removes an envelope from all buckets.
func (s *BBoltEnvelopeStore) RemoveEnvelope(ctx context.Context, envID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Get envelope first to find keys
		envBytes := tx.Bucket(model.BucketEnvelopes).Get([]byte(envID))
		if envBytes != nil {
			env, err := s.codec.DecodeEnvelope(envBytes)
			if err == nil {
				// Remove from destination index
				destKey := model.DestinationIndexKey(env.DestinationID, envID)
				tx.Bucket(model.BucketByDestination).Delete(destKey)

				// Remove from expiry index
				expiryKey := model.ExpiryIndexKey(env.ExpiresAt, envID)
				tx.Bucket(model.BucketByExpiry).Delete(expiryKey)
			}
		}

		// Remove from envelopes bucket
		tx.Bucket(model.BucketEnvelopes).Delete([]byte(envID))

		// Note: We don't remove seen entries - they're for historical tracking

		return nil
	})
}

// GetExpiredEnvelopes returns envelopes that have expired.
func (s *BBoltEnvelopeStore) GetExpiredEnvelopes(ctx context.Context, before time.Time) ([]*model.Envelope, error) {
	var envelopes []*model.Envelope
	beforeStr := before.Format(time.RFC3339Nano)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(model.BucketByExpiry).Cursor()

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

			envID := string(v)
			envBytes := tx.Bucket(model.BucketEnvelopes).Get([]byte(envID))
			if envBytes == nil {
				continue
			}

			env, err := s.codec.DecodeEnvelope(envBytes)
			if err != nil {
				continue
			}

			envelopes = append(envelopes, env)
		}
		return nil
	})

	return envelopes, err
}

// MarkSeen marks an envelope as seen by a relay.
func (s *BBoltEnvelopeStore) MarkSeen(ctx context.Context, envID string, relayID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		seenKey := model.SeenKey(envID, relayID)
		return tx.Bucket(model.BucketSeen).Put(seenKey, []byte(time.Now().Format(time.RFC3339Nano)))
	})
}

// IsSeenBy checks if an envelope has been seen by a specific relay.
func (s *BBoltEnvelopeStore) IsSeenBy(ctx context.Context, envID string, relayID string) (bool, error) {
	var seen bool
	err := s.db.View(func(tx *bolt.Tx) error {
		seenKey := model.SeenKey(envID, relayID)
		result := tx.Bucket(model.BucketSeen).Get(seenKey)
		seen = result != nil
		return nil
	})
	return seen, err
}

// GetAllDestinationIDs returns all unique destination IDs with envelopes.
func (s *BBoltEnvelopeStore) GetAllDestinationIDs(ctx context.Context) ([]string, error) {
	var destIDs []string
	err := s.db.View(func(tx *bolt.Tx) error {
		seen := make(map[string]bool)
		c := tx.Bucket(model.BucketByDestination).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			// Extract destination ID from key (format: destination\x00msgid)
			parts := bytes.Split(k, []byte{0})
			if len(parts) < 1 {
				continue
			}
			destID := string(parts[0])
			if !seen[destID] {
				seen[destID] = true
				destIDs = append(destIDs, destID)
			}
		}
		return nil
	})
	return destIDs, err
}

// GetLastSweepTime returns the last time the sweeper ran.
func (s *BBoltEnvelopeStore) GetLastSweepTime(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(model.BucketMeta).Get(model.MetaLastSweep)
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
func (s *BBoltEnvelopeStore) SetLastSweepTime(ctx context.Context, t time.Time) error {
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
		return tx.Bucket(model.BucketMeta).Put(model.MetaLastSweep, v)
	})
}
