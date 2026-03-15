package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/xerrors"

	"github.com/natemellendorf/aethos-relay/internal/gossipv1"
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
			model.BucketEnvelopeExpiry,
			model.BucketSeen,
			model.BucketRelayIngest,
			model.BucketMeta,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return xerrors.Errorf("failed to create bucket %s: %w", string(b), err)
			}
		}

		if err := s.backfillEnvelopeExpiryIndex(tx); err != nil {
			return xerrors.Errorf("failed to backfill envelope expiry index: %w", err)
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to initialize envelope buckets: %w", err)
	}

	return nil
}

func (s *BBoltEnvelopeStore) backfillEnvelopeExpiryIndex(tx *bolt.Tx) error {
	envelopesBucket := tx.Bucket(model.BucketEnvelopes)
	envelopeExpiryBucket := tx.Bucket(model.BucketEnvelopeExpiry)
	if envelopesBucket == nil || envelopeExpiryBucket == nil {
		return fmt.Errorf("required envelope buckets are missing")
	}

	cursor := envelopesBucket.Cursor()
	for envID, encodedEnvelope := cursor.First(); envID != nil; envID, encodedEnvelope = cursor.Next() {
		if envelopeExpiryBucket.Get(envID) != nil {
			continue
		}

		envelope, err := s.codec.DecodeEnvelope(encodedEnvelope)
		if err != nil {
			continue
		}
		if err := envelopeExpiryBucket.Put(envID, encodeUnixNano(envelope.ExpiresAt.UTC().UnixNano())); err != nil {
			return err
		}
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
		envelopesBucket := tx.Bucket(model.BucketEnvelopes)
		byDestinationBucket := tx.Bucket(model.BucketByDestination)
		byExpiryBucket := tx.Bucket(model.BucketByExpiry)
		envelopeExpiryBucket := tx.Bucket(model.BucketEnvelopeExpiry)

		existingBytes := envelopesBucket.Get([]byte(env.ID))
		if existingBytes != nil {
			existing, decodeErr := s.codec.DecodeEnvelope(existingBytes)
			if decodeErr == nil {
				existingDestKey := model.DestinationIndexKey(existing.DestinationID, env.ID)
				newDestKey := model.DestinationIndexKey(env.DestinationID, env.ID)
				if !bytes.Equal(existingDestKey, newDestKey) {
					_ = byDestinationBucket.Delete(existingDestKey)
				}

				existingExpiryKey := model.ExpiryIndexKey(existing.ExpiresAt, env.ID)
				newExpiryKey := model.ExpiryIndexKey(env.ExpiresAt, env.ID)
				if !bytes.Equal(existingExpiryKey, newExpiryKey) {
					_ = byExpiryBucket.Delete(existingExpiryKey)
				}
			}
		}

		// Validate envelope before persisting
		if err := env.Validate(); err != nil {
			return xerrors.Errorf("invalid envelope: %w", err)
		}

		// Store the envelope
		envBytes, err := s.codec.EncodeEnvelope(env)
		if err != nil {
			return xerrors.Errorf("failed to encode envelope: %w", err)
		}
		if err := envelopesBucket.Put([]byte(env.ID), envBytes); err != nil {
			return xerrors.Errorf("failed to store envelope: %w", err)
		}

		// Add to destination index
		destKey := model.DestinationIndexKey(env.DestinationID, env.ID)
		if err := byDestinationBucket.Put(destKey, []byte(env.ID)); err != nil {
			return xerrors.Errorf("failed to add to destination index: %w", err)
		}

		// Add to expiry index
		expiryKey := model.ExpiryIndexKey(env.ExpiresAt, env.ID)
		if err := byExpiryBucket.Put(expiryKey, []byte(env.ID)); err != nil {
			return xerrors.Errorf("failed to add to expiry index: %w", err)
		}
		if err := envelopeExpiryBucket.Put([]byte(env.ID), encodeUnixNano(env.ExpiresAt.UTC().UnixNano())); err != nil {
			return xerrors.Errorf("failed to add to envelope expiry index: %w", err)
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

// GetEnvelopeIDsByDestinationPage retrieves envelope IDs for a destination with deterministic cursor paging.
func (s *BBoltEnvelopeStore) GetEnvelopeIDsByDestinationPage(ctx context.Context, destID string, afterCursor string, limit int) ([]string, string, int, []string, error) {
	if limit <= 0 {
		return []string{}, "", 0, []string{}, nil
	}
	if afterCursor != "" && !gossipv1.IsDigestHexID(afterCursor) {
		return nil, "", 0, nil, fmt.Errorf("destination cursor must be 64 lowercase hex chars")
	}

	prefix := destinationIndexPrefix(destID)
	pageIDs := make([]string, 0, limit)
	eligibleIDs := make([]string, 0, limit)
	nowUnixNano := time.Now().UTC().UnixNano()
	totalCount := 0

	err := s.db.View(func(tx *bolt.Tx) error {
		destinationBucket := tx.Bucket(model.BucketByDestination)
		envelopeExpiryBucket := tx.Bucket(model.BucketEnvelopeExpiry)
		if destinationBucket == nil || envelopeExpiryBucket == nil {
			return fmt.Errorf("required envelope buckets are missing")
		}

		cursor := destinationBucket.Cursor()
		for k, _ := cursor.Seek(prefix); k != nil; k, _ = cursor.Next() {
			if !bytes.HasPrefix(k, prefix) {
				break
			}

			itemID, ok := destinationIndexItemID(prefix, k)
			if !ok {
				continue
			}
			if !isEligibleDestinationItem(itemID, envelopeExpiryBucket.Get([]byte(itemID)), nowUnixNano) {
				continue
			}

			totalCount++
			eligibleIDs = append(eligibleIDs, itemID)
		}

		startKey := prefix
		if afterCursor != "" {
			startKey = append(append([]byte(nil), prefix...), afterCursor...)
		}
		for k, _ := cursor.Seek(startKey); k != nil && len(pageIDs) < limit; k, _ = cursor.Next() {
			if !bytes.HasPrefix(k, prefix) {
				break
			}

			itemID, ok := destinationIndexItemID(prefix, k)
			if !ok {
				continue
			}
			if afterCursor != "" && itemID == afterCursor {
				continue
			}
			if !isEligibleDestinationItem(itemID, envelopeExpiryBucket.Get([]byte(itemID)), nowUnixNano) {
				continue
			}

			pageIDs = append(pageIDs, itemID)
		}

		return nil
	})

	if err != nil {
		return nil, "", 0, nil, err
	}

	if totalCount == 0 {
		return []string{}, "", 0, []string{}, nil
	}

	nextCursor := ""
	if len(pageIDs) > 0 {
		nextCursor = pageIDs[len(pageIDs)-1]
	}

	return pageIDs, nextCursor, totalCount, eligibleIDs, nil
}

// GetAllEnvelopeIDs returns all unique, non-expired digest envelope IDs.
func (s *BBoltEnvelopeStore) GetAllEnvelopeIDs(ctx context.Context) ([]string, error) {
	nowUnixNano := time.Now().UTC().UnixNano()
	eligibleByID := make(map[string]struct{})

	err := s.db.View(func(tx *bolt.Tx) error {
		destinationBucket := tx.Bucket(model.BucketByDestination)
		envelopeExpiryBucket := tx.Bucket(model.BucketEnvelopeExpiry)
		if destinationBucket == nil || envelopeExpiryBucket == nil {
			return fmt.Errorf("required envelope buckets are missing")
		}

		cursor := destinationBucket.Cursor()
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			itemID, ok := destinationIndexItemIDFromKey(k)
			if !ok {
				continue
			}
			if !isEligibleDestinationItem(itemID, envelopeExpiryBucket.Get([]byte(itemID)), nowUnixNano) {
				continue
			}

			eligibleByID[itemID] = struct{}{}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(eligibleByID) == 0 {
		return []string{}, nil
	}

	eligibleIDs := make([]string, 0, len(eligibleByID))
	for itemID := range eligibleByID {
		eligibleIDs = append(eligibleIDs, itemID)
	}
	gossipv1.SortDigestHexIDs(eligibleIDs)

	return eligibleIDs, nil
}

// RemoveEnvelope removes an envelope from all buckets.
func (s *BBoltEnvelopeStore) RemoveEnvelope(ctx context.Context, envID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		envelopeExpiryBucket := tx.Bucket(model.BucketEnvelopeExpiry)
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
		envelopeExpiryBucket.Delete([]byte(envID))

		// Note: We don't remove seen entries - they're for historical tracking

		return nil
	})
}

func destinationIndexPrefix(destinationID string) []byte {
	prefix := make([]byte, len(destinationID)+1)
	copy(prefix, destinationID)
	prefix[len(destinationID)] = byte(0)
	return prefix
}

func destinationIndexItemID(prefix []byte, key []byte) (string, bool) {
	if len(key) <= len(prefix) || !bytes.HasPrefix(key, prefix) {
		return "", false
	}
	return string(key[len(prefix):]), true
}

func destinationIndexItemIDFromKey(key []byte) (string, bool) {
	separator := bytes.IndexByte(key, byte(0))
	if separator < 0 || separator+1 >= len(key) {
		return "", false
	}
	return string(key[separator+1:]), true
}

func isEligibleDestinationItem(itemID string, encodedExpiry []byte, nowUnixNano int64) bool {
	if !gossipv1.IsDigestHexID(itemID) {
		return false
	}
	expiresAtUnixNano, ok := decodeUnixNano(encodedExpiry)
	if !ok {
		return false
	}
	if nowUnixNano > expiresAtUnixNano {
		return false
	}
	return true
}

func encodeUnixNano(unixNano int64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, uint64(unixNano))
	return out
}

func decodeUnixNano(raw []byte) (int64, bool) {
	if len(raw) < 8 {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(raw[:8])), true
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

// MarkSeenAndRelayIngestEmitted persists seen bookkeeping and ingest marker in one transaction.
// Returns true only when ingest marker is newly created in this call.
func (s *BBoltEnvelopeStore) MarkSeenAndRelayIngestEmitted(ctx context.Context, itemID string, relayIDs []string) (bool, error) {
	if itemID == "" {
		return false, fmt.Errorf("relay ingest item_id is required")
	}

	newlyMarked := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		seenBucket := tx.Bucket(model.BucketSeen)
		for _, relayID := range relayIDs {
			if relayID == "" {
				continue
			}
			seenKey := model.SeenKey(itemID, relayID)
			if err := seenBucket.Put(seenKey, []byte(time.Now().Format(time.RFC3339Nano))); err != nil {
				return err
			}
		}

		markerBucket := tx.Bucket(model.BucketRelayIngest)
		markerKey := model.RelayIngestKey(itemID)
		if markerBucket.Get(markerKey) != nil {
			return nil
		}
		newlyMarked = true
		return markerBucket.Put(markerKey, []byte(time.Now().Format(time.RFC3339Nano)))
	})
	if err != nil {
		return false, err
	}

	return newlyMarked, nil
}

// MarkRelayIngestEmitted marks RELAY_INGEST as durably emitted for item_id.
// Returns true only when marker is newly created in this call.
func (s *BBoltEnvelopeStore) MarkRelayIngestEmitted(ctx context.Context, itemID string) (bool, error) {
	if itemID == "" {
		return false, fmt.Errorf("relay ingest item_id is required")
	}

	newlyMarked := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(model.BucketRelayIngest)
		markerKey := model.RelayIngestKey(itemID)
		if bucket.Get(markerKey) != nil {
			return nil
		}
		newlyMarked = true
		return bucket.Put(markerKey, []byte(time.Now().Format(time.RFC3339Nano)))
	})
	if err != nil {
		return false, err
	}

	return newlyMarked, nil
}

// IsRelayIngestEmitted checks whether RELAY_INGEST marker exists for item_id.
func (s *BBoltEnvelopeStore) IsRelayIngestEmitted(ctx context.Context, itemID string) (bool, error) {
	if itemID == "" {
		return false, fmt.Errorf("relay ingest item_id is required")
	}

	var emitted bool
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(model.BucketRelayIngest)
		emitted = bucket.Get(model.RelayIngestKey(itemID)) != nil
		return nil
	})
	if err != nil {
		return false, err
	}

	return emitted, nil
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
