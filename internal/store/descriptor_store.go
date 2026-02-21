package store

import (
	"context"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/xerrors"

	"github.com/natemellendorf/aethos-relay/internal/model"
)

var (
	// BucketDescriptors stores relay descriptors by relay_id.
	BucketDescriptors = []byte("descriptors")

	// BucketDescriptorExpiryIndex stores descriptors by expiry time.
	BucketDescriptorExpiryIndex = []byte("descriptor_expiry_index")

	// BucketDescriptorPeerIndex tracks descriptors per peer for rate limiting.
	BucketDescriptorPeerIndex = []byte("descriptor_peer_index")
)

// DescriptorStore defines the interface for descriptor persistence.
type DescriptorStore interface {
	// Open opens the descriptor store.
	Open() error

	// Close closes the descriptor store.
	Close() error

	// PutDescriptor stores or updates a descriptor.
	// Deduplication: if the descriptor exists, updates last_seen_at.
	PutDescriptor(ctx context.Context, d *model.RelayDescriptor) error

	// GetDescriptor retrieves a descriptor by relay_id.
	GetDescriptor(ctx context.Context, relayID string) (*model.RelayDescriptor, error)

	// GetAllDescriptors returns all descriptors.
	GetAllDescriptors(ctx context.Context) ([]*model.RelayDescriptor, error)

	// RemoveDescriptor removes a descriptor.
	RemoveDescriptor(ctx context.Context, relayID string) error

	// GetExpiredDescriptors returns descriptors that have expired.
	GetExpiredDescriptors(ctx context.Context, before time.Time) ([]*model.RelayDescriptor, error)

	// GetDescriptorsForPeer returns descriptors advertised by a specific peer.
	GetDescriptorsForPeer(ctx context.Context, peerID string) ([]*model.RelayDescriptor, error)

	// CountDescriptors returns the total number of descriptors.
	CountDescriptors(ctx context.Context) (int, error)

	// GetPeerDescriptorCount returns the number of descriptors from a peer in the last hour.
	GetPeerDescriptorCount(ctx context.Context, peerID string) (int, error)
}

// BBoltDescriptorStore implements DescriptorStore using bbolt.
type BBoltDescriptorStore struct {
	path string
	db   *bolt.DB
}

// NewBBoltDescriptorStore creates a new descriptor store.
func NewBBoltDescriptorStore(path string) *BBoltDescriptorStore {
	return &BBoltDescriptorStore{path: path}
}

// Open opens the bbolt database for descriptors.
func (s *BBoltDescriptorStore) Open() error {
	db, err := bolt.Open(s.path, 0600, nil)
	if err != nil {
		return xerrors.Errorf("failed to open descriptor store: %w", err)
	}
	s.db = db

	// Create buckets if they don't exist
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{
			BucketDescriptors,
			BucketDescriptorExpiryIndex,
			BucketDescriptorPeerIndex,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return xerrors.Errorf("failed to create bucket %s: %w", string(b), err)
			}
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to initialize descriptor buckets: %w", err)
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
func (s *BBoltDescriptorStore) PutDescriptor(ctx context.Context, d *model.RelayDescriptor) error {
	// Validate TTL clamping
	if d.ExpiresAt.Sub(d.LastSeenAt) > model.MAX_DESCRIPTOR_TTL {
		d.ExpiresAt = d.LastSeenAt.Add(model.MAX_DESCRIPTOR_TTL)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		// Check if descriptor already exists (for deduplication)
		existing := tx.Bucket(BucketDescriptors).Get([]byte(d.RelayID))
		var isUpdate bool
		if existing != nil {
			isUpdate = true
		}

		// If this is a new descriptor, enforce registry cap
		if !isUpdate {
			count := tx.Bucket(BucketDescriptors).Stats().KeyN
			if count >= model.MAX_TOTAL_DESCRIPTORS {
				return model.ErrDescriptorRegistryFull
			}
		}

		// Encode and store the descriptor
		descBytes, err := EncodeDescriptor(d)
		if err != nil {
			return xerrors.Errorf("failed to encode descriptor: %w", err)
		}

		if err := tx.Bucket(BucketDescriptors).Put([]byte(d.RelayID), descBytes); err != nil {
			return xerrors.Errorf("failed to store descriptor: %w", err)
		}

		// Update expiry index - need to delete old key first if updating
		if isUpdate {
			// For updates, we'll just overwrite - cursor-based cleanup happens during sweeps
		}
		expiryKey := EncodeDescriptorExpiryKey(d.RelayID, d.ExpiresAt)
		if err := tx.Bucket(BucketDescriptorExpiryIndex).Put(expiryKey, []byte(d.RelayID)); err != nil {
			return xerrors.Errorf("failed to update expiry index: %w", err)
		}

		// Update peer index for rate limiting (only for new descriptors)
		if !isUpdate && d.AdvertisedBy != "" {
			peerKey := EncodeDescriptorPeerKey(d.AdvertisedBy, d.FirstSeenAt)
			if err := tx.Bucket(BucketDescriptorPeerIndex).Put(peerKey, []byte(d.RelayID)); err != nil {
				return xerrors.Errorf("failed to update peer index: %w", err)
			}
		}

		return nil
	})
}

// GetDescriptor retrieves a descriptor by relay_id.
func (s *BBoltDescriptorStore) GetDescriptor(ctx context.Context, relayID string) (*model.RelayDescriptor, error) {
	var desc *model.RelayDescriptor
	err := s.db.View(func(tx *bolt.Tx) error {
		descBytes := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
		if descBytes == nil {
			desc = nil
			return nil
		}
		var decodeErr error
		desc, decodeErr = DecodeDescriptor(descBytes)
		if decodeErr != nil {
			return xerrors.Errorf("failed to decode descriptor: %w", decodeErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if desc == nil {
		return nil, xerrors.New("descriptor not found")
	}
	return desc, nil
}

// GetAllDescriptors returns all descriptors.
func (s *BBoltDescriptorStore) GetAllDescriptors(ctx context.Context) ([]*model.RelayDescriptor, error) {
	var descriptors []*model.RelayDescriptor
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptors).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			desc, err := DecodeDescriptor(v)
			if err != nil {
				continue
			}
			// Skip expired descriptors
			if desc.IsExpired() {
				continue
			}
			descriptors = append(descriptors, desc)
		}
		return nil
	})
	return descriptors, err
}

// RemoveDescriptor removes a descriptor.
func (s *BBoltDescriptorStore) RemoveDescriptor(ctx context.Context, relayID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Get descriptor first for cleanup
		descBytes := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
		if descBytes != nil {
			desc, err := DecodeDescriptor(descBytes)
			if err == nil {
				// Remove from expiry index
				expiryKey := EncodeDescriptorExpiryKey(relayID, desc.ExpiresAt)
				tx.Bucket(BucketDescriptorExpiryIndex).Delete(expiryKey)

				// Remove from peer index
				if desc.AdvertisedBy != "" {
					peerKey := EncodeDescriptorPeerKey(desc.AdvertisedBy, desc.FirstSeenAt)
					tx.Bucket(BucketDescriptorPeerIndex).Delete(peerKey)
				}
			}
		}

		// Remove from descriptors bucket
		tx.Bucket(BucketDescriptors).Delete([]byte(relayID))
		return nil
	})
}

// GetExpiredDescriptors returns descriptors that have expired.
func (s *BBoltDescriptorStore) GetExpiredDescriptors(ctx context.Context, before time.Time) ([]*model.RelayDescriptor, error) {
	var descriptors []*model.RelayDescriptor
	beforeStr := before.Format(time.RFC3339Nano)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptorExpiryIndex).Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Parse the key to extract expires_at
			parts := splitKey(k)
			if len(parts) < 2 {
				continue
			}
			expiresAtStr := parts[0]
			if expiresAtStr > beforeStr {
				break // Not yet expired, and keys are sorted so we can break
			}

			relayID := string(v)
			descBytes := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
			if descBytes == nil {
				continue
			}

			desc, err := DecodeDescriptor(descBytes)
			if err != nil {
				continue
			}

			descriptors = append(descriptors, desc)
		}
		return nil
	})

	return descriptors, err
}

// GetDescriptorsForPeer returns descriptors advertised by a specific peer.
func (s *BBoltDescriptorStore) GetDescriptorsForPeer(ctx context.Context, peerID string) ([]*model.RelayDescriptor, error) {
	var descriptors []*model.RelayDescriptor

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptorPeerIndex).Cursor()
		prefix := []byte(peerID + "\x00")

		for k, v := c.Seek(prefix); k != nil; k, v = c.Next() {
			if len(k) < len(prefix) || string(k[:len(prefix)]) != string(prefix) {
				break
			}

			relayID := string(v)
			descBytes := tx.Bucket(BucketDescriptors).Get([]byte(relayID))
			if descBytes == nil {
				continue
			}

			desc, err := DecodeDescriptor(descBytes)
			if err != nil {
				continue
			}

			descriptors = append(descriptors, desc)
		}
		return nil
	})

	return descriptors, err
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

// GetPeerDescriptorCount returns the number of descriptors from a peer in the last hour.
func (s *BBoltDescriptorStore) GetPeerDescriptorCount(ctx context.Context, peerID string) (int, error) {
	var count int
	oneHourAgo := time.Now().Add(-time.Hour)

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(BucketDescriptorPeerIndex).Cursor()
		prefix := []byte(peerID + "\x00")

		for k, _ := c.Seek(prefix); k != nil; k, _ = c.Next() {
			if len(k) < len(prefix) || string(k[:len(prefix)]) != string(prefix) {
				break
			}

			// Parse timestamp from key
			parts := splitKey(k)
			if len(parts) < 2 {
				continue
			}
			timestampStr := parts[1]
			t, err := time.Parse(time.RFC3339Nano, timestampStr)
			if err != nil {
				continue
			}
			if t.After(oneHourAgo) {
				count++
			}
		}
		return nil
	})

	return count, err
}

// splitKey splits a composite key by null byte.
func splitKey(key []byte) []string {
	var parts []string
	var current []byte
	for _, b := range key {
		if b == 0 {
			parts = append(parts, string(current))
			current = nil
		} else {
			current = append(current, b)
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}
	return parts
}
