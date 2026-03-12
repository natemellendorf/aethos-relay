package federation

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestPeerBatcherEnqueueAndDrain(t *testing.T) {
	config := &TARConfig{
		BatchInterval: 10 * time.Millisecond,
		BatchJitter:   0,
		BatchMax:      5,
	}
	config.Validate()

	batcher := NewPeerBatcher("test-peer", 100, config)

	var sent [][]byte
	mu := new(sync.Mutex)

	sendFunc := func(data []byte) {
		mu.Lock()
		sent = append(sent, data)
		mu.Unlock()
	}

	batcher.Start(sendFunc)

	// Enqueue some frames
	for i := 0; i < 3; i++ {
		if !batcher.Enqueue([]byte("test-frame")) {
			t.Errorf("Failed to enqueue frame %d", i)
		}
	}

	// Wait for batch to drain
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(sent) != 3 {
		t.Errorf("Expected 3 frames sent, got %d", len(sent))
	}
	mu.Unlock()

	batcher.Stop()
}

func TestPeerBatcherJitter(t *testing.T) {
	config := &TARConfig{
		BatchInterval: 50 * time.Millisecond,
		BatchJitter:   20 * time.Millisecond,
		BatchMax:      10,
	}
	config.Validate()

	batcher := NewPeerBatcher("test-peer", 100, config)

	var sendTimes []time.Time
	mu := new(sync.Mutex)

	sendFunc := func(data []byte) {
		mu.Lock()
		sendTimes = append(sendTimes, time.Now())
		mu.Unlock()
	}

	batcher.Start(sendFunc)

	// Enqueue frames at regular intervals
	for i := 0; i < 3; i++ {
		batcher.Enqueue([]byte("test-frame"))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for batches to drain
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if len(sendTimes) < 2 {
		t.Logf("Expected multiple batches due to jitter, got %d batches", len(sendTimes))
	}
	mu.Unlock()

	batcher.Stop()
}

func TestPeerBatcherQueueFull(t *testing.T) {
	config := &TARConfig{
		BatchInterval: 1 * time.Second,
		BatchJitter:   0,
		BatchMax:      1,
	}
	config.Validate()

	// Create batcher with very small queue
	batcher := NewPeerBatcher("test-peer", 2, config)

	// Fill the queue
	batcher.Enqueue([]byte("frame1"))
	batcher.Enqueue([]byte("frame2"))

	// This should fail (queue full)
	if batcher.Enqueue([]byte("frame3")) {
		t.Error("Expected enqueue to fail when queue is full")
	}

	batcher.Stop()
}

func TestPeerBatcherQueueLength(t *testing.T) {
	config := &TARConfig{
		BatchInterval: 1 * time.Second,
		BatchJitter:   0,
		BatchMax:      10,
	}

	batcher := NewPeerBatcher("test-peer", 100, config)

	if batcher.QueueLength() != 0 {
		t.Error("Expected empty queue initially")
	}

	batcher.Enqueue([]byte("frame1"))
	batcher.Enqueue([]byte("frame2"))

	if batcher.QueueLength() != 2 {
		t.Errorf("Expected queue length 2, got %d", batcher.QueueLength())
	}

	batcher.Stop()
}

func TestPeerBatcherCoverFramesIncludeLegacyAndCanonicalTimestamps(t *testing.T) {
	config := &TARConfig{
		CoverEnabled: true,
		CoverMax:     1,
	}
	config.Validate()

	batcher := NewPeerBatcher("test-peer", 10, config)
	batcher.randIntn = func(max int) int {
		if max <= 1 {
			return 0
		}
		return 1
	}
	batcher.randInt63 = func() int64 {
		return 42
	}

	type coverFrame struct {
		Type      string `json:"type"`
		Timestamp int64  `json:"ts"`
		SentAt    uint64 `json:"sent_at,omitempty"`
		Nonce     int64  `json:"nonce"`
	}

	var sent []coverFrame
	sendFunc := func(data []byte) {
		var cover coverFrame
		if err := json.Unmarshal(data, &cover); err != nil {
			t.Fatalf("decode cover frame: %v", err)
		}
		sent = append(sent, cover)
	}

	batcher.sendCoverFrames(sendFunc)

	if len(sent) != 1 {
		t.Fatalf("expected exactly one deterministic cover frame, got %d", len(sent))
	}

	cover := sent[0]
	if cover.Type != "COVER" {
		t.Fatalf("expected COVER frame, got %q", cover.Type)
	}
	if cover.Timestamp == 0 {
		t.Fatal("expected legacy ts field to be populated")
	}
	if cover.SentAt == 0 {
		t.Fatal("expected canonical sent_at field to be populated")
	}
	if cover.SentAt < 1_000_000_000_000 {
		t.Fatalf("expected sent_at to be millisecond-scale, got %d", cover.SentAt)
	}
	if cover.SentAt/1000 != uint64(cover.Timestamp) {
		t.Fatalf("expected sent_at and ts to align, got sent_at=%d ts=%d", cover.SentAt, cover.Timestamp)
	}
	if cover.Nonce != 42 {
		t.Fatalf("expected deterministic nonce, got %d", cover.Nonce)
	}
}
