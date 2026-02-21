package federation

import (
	"context"
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
)

// TARConfig holds configuration for Traffic Analysis Resistance.
type TARConfig struct {
	// Batching
	BatchInterval time.Duration // Base tick interval (default 500ms)
	BatchJitter   time.Duration // Jitter range (default 250ms)
	BatchMax      int           // Max frames per batch (default 10)

	// Padding
	PaddingEnabled bool  // Enable payload padding
	PadBuckets     []int // Bucket sizes for padding

	// Cover frames
	CoverEnabled bool // Enable cover frames
	CoverMax     int  // Max cover frames when queue empty
}

// DefaultTARConfig returns default TAR configuration.
func DefaultTARConfig() *TARConfig {
	return &TARConfig{
		BatchInterval:  500 * time.Millisecond,
		BatchJitter:    250 * time.Millisecond,
		BatchMax:       10,
		PaddingEnabled: false,
		PadBuckets:     []int{1024, 4096, 16384, 65536},
		CoverEnabled:   false,
		CoverMax:       3,
	}
}

// Validate validates the TAR configuration.
func (c *TARConfig) Validate() error {
	if c.BatchInterval <= 0 {
		c.BatchInterval = 500 * time.Millisecond
	}
	if c.BatchJitter < 0 {
		c.BatchJitter = 250 * time.Millisecond
	}
	if c.BatchMax <= 0 {
		c.BatchMax = 10
	}
	if c.PadBuckets == nil || len(c.PadBuckets) == 0 {
		c.PadBuckets = []int{1024, 4096, 16384, 65536}
	}
	// Sort buckets ascending
	for i := 0; i < len(c.PadBuckets)-1; i++ {
		for j := i + 1; j < len(c.PadBuckets); j++ {
			if c.PadBuckets[i] > c.PadBuckets[j] {
				c.PadBuckets[i], c.PadBuckets[j] = c.PadBuckets[j], c.PadBuckets[i]
			}
		}
	}
	if c.CoverMax < 0 {
		c.CoverMax = 0
	}
	return nil
}

// PeerBatcher manages batching for a single peer.
type PeerBatcher struct {
	peerID   string
	queue    chan []byte
	config   *TARConfig
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	closedMu sync.Mutex
}

// NewPeerBatcher creates a new peer batcher.
func NewPeerBatcher(peerID string, queueSize int, config *TARConfig) *PeerBatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerBatcher{
		peerID: peerID,
		queue:  make(chan []byte, queueSize),
		config: config,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Enqueue adds a frame to the batch queue.
func (b *PeerBatcher) Enqueue(frame []byte) bool {
	b.closedMu.Lock()
	defer b.closedMu.Unlock()

	if b.closed {
		return false
	}

	select {
	case b.queue <- frame:
		return true
	default:
		// Queue full, drop frame
		metrics.IncrementDropped()
		return false
	}
}

// Start starts the batcher loop.
func (b *PeerBatcher) Start(sendFunc func([]byte)) {
	go b.batcherLoop(sendFunc)
}

// batcherLoop drains the queue at tick intervals with jitter.
func (b *PeerBatcher) batcherLoop(sendFunc func([]byte)) {
	ticker := time.NewTicker(b.config.BatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			// Drain remaining queue before exit
			b.drainQueue(sendFunc, -1)
			return
		case <-ticker.C:
			// Calculate jitter (if jitter is 0, skip jitter calculation)
			adjustedInterval := b.config.BatchInterval
			if b.config.BatchJitter > 0 {
				jitter := time.Duration(rand.Int63n(int64(2*b.config.BatchJitter))) - b.config.BatchJitter
				adjustedInterval = b.config.BatchInterval + jitter
			}

			// Reset ticker with jittered interval
			ticker.Reset(adjustedInterval)

			// Drain up to BatchMax frames
			b.drainQueue(sendFunc, b.config.BatchMax)

			// If queue empty and cover frames enabled, send cover frames
			if b.config.CoverEnabled && len(b.queue) == 0 {
				b.sendCoverFrames(sendFunc)
			}
		}
	}
}

// drainQueue drains frames from the queue.
func (b *PeerBatcher) drainQueue(sendFunc func([]byte), max int) {
	drained := 0
	for {
		select {
		case frame := <-b.queue:
			sendFunc(frame)
			drained++
			if max > 0 && drained >= max {
				return
			}
		default:
			return
		}
	}
}

// sendCoverFrames sends cover frames when queue is empty.
func (b *PeerBatcher) sendCoverFrames(sendFunc func([]byte)) {
	if b.config.CoverMax == 0 {
		return
	}

	// Random number of cover frames (0 to CoverMax)
	numCover := rand.Intn(b.config.CoverMax + 1)
	if numCover == 0 {
		return
	}

	for i := 0; i < numCover; i++ {
		cover := model.RelayCoverFrame{
			Type:      model.FrameTypeRelayCover,
			Timestamp: time.Now().Unix(),
			Nonce:     rand.Int63(),
		}
		data, err := json.Marshal(cover)
		if err != nil {
			continue
		}
		sendFunc(data)
	}
}

// Stop stops the batcher.
func (b *PeerBatcher) Stop() {
	b.closedMu.Lock()
	defer b.closedMu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	b.cancel()
}

// QueueLength returns the current queue length.
func (b *PeerBatcher) QueueLength() int {
	return len(b.queue)
}

// PadPayload pads a payload to the nearest bucket size.
func PadPayload(payload []byte, buckets []int) []byte {
	if len(buckets) == 0 {
		return payload
	}

	payloadLen := len(payload)
	for _, bucket := range buckets {
		if bucket >= payloadLen {
			// Found the nearest bucket
			if bucket == payloadLen {
				return payload
			}
			// Pad with random bytes
			padded := make([]byte, bucket)
			copy(padded, payload)
			// Fill remaining with random bytes
			for i := payloadLen; i < bucket; i++ {
				padded[i] = byte(rand.Int())
			}
			return padded
		}
	}
	// No bucket found, return original
	return payload
}

// ForwardingConfig holds configuration for forwarding strategy.
type ForwardingConfig struct {
	TopK        int           // Number of top peers to forward to (default 2)
	ExploreProb float64       // Probability of exploring a non-topK peer (default 0.1)
	MaxHops     int           // Maximum hop count (default 3)
	Timeout     time.Duration // Ack timeout (default 10s)
	BackoffBase time.Duration // Base backoff delay (default 1s)
	BackoffMax  time.Duration // Max backoff delay (default 60s)
}

// DefaultForwardingConfig returns default forwarding configuration.
func DefaultForwardingConfig() *ForwardingConfig {
	return &ForwardingConfig{
		TopK:        2,
		ExploreProb: 0.1,
		MaxHops:     3,
		Timeout:     10 * time.Second,
		BackoffBase: 1 * time.Second,
		BackoffMax:  60 * time.Second,
	}
}

// Validate validates the forwarding configuration.
func (c *ForwardingConfig) Validate() error {
	if c.TopK <= 0 {
		c.TopK = 2
	}
	if c.ExploreProb < 0 || c.ExploreProb > 1 {
		c.ExploreProb = 0.1
	}
	if c.MaxHops <= 0 {
		c.MaxHops = 3
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 1 * time.Second
	}
	if c.BackoffMax < c.BackoffBase {
		c.BackoffMax = 60 * time.Second
	}
	return nil
}

// ForwardingStrategy selects peers for forwarding based on scores.
type ForwardingStrategy struct {
	config *ForwardingConfig
}

// NewForwardingStrategy creates a new forwarding strategy.
func NewForwardingStrategy(config *ForwardingConfig) *ForwardingStrategy {
	return &ForwardingStrategy{config: config}
}

// SelectPeers selects peers for forwarding based on scores.
// Returns selected peer IDs, excluding the origin.
func (s *ForwardingStrategy) SelectPeers(peerMetrics map[string]*model.PeerMetrics, originID string) []string {
	// Filter out origin and unhealthy peers
	var candidates []*model.PeerMetrics
	for id, metrics := range peerMetrics {
		if id == originID {
			continue
		}
		if metrics.IsHealthy() {
			candidates = append(candidates, metrics)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by score descending
	model.SortByPeerScore(candidates)

	// Select top K
	topK := s.config.TopK
	if topK > len(candidates) {
		topK = len(candidates)
	}

	var selected []string
	for i := 0; i < topK; i++ {
		selected = append(selected, candidates[i].PeerID)
	}

	// Exploration: possibly add one non-topK peer
	if s.config.ExploreProb > 0 && len(candidates) > topK {
		// Use deterministic RNG based on peer seed for consistency
		rng := rand.New(rand.NewSource(candidates[topK].Seed))
		if rng.Float64() < s.config.ExploreProb {
			selected = append(selected, candidates[topK].PeerID)
		}
	}

	return selected
}
