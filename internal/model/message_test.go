package model

import (
	"sync"
	"testing"
	"time"
)

// mockConn implements a minimal connection interface for testing.
type mockConn struct{}

func (m *mockConn) WriteJSON(v interface{}) error                       { return nil }
func (m *mockConn) ReadJSON(v interface{}) error                        { return nil }
func (m *mockConn) WriteMessage(msgType int, data []byte) error         { return nil }
func (m *mockConn) ReadMessage() (messageType int, p []byte, err error) { return 0, nil, nil }
func (m *mockConn) Close() error                                        { return nil }

// TestClientRegistryConcurrentAccess tests that ClientRegistry is safe for concurrent access.
// This test uses the race detector to verify no data races occur.
func TestClientRegistryConcurrentAccess(t *testing.T) {
	registry := NewClientRegistry()

	// Start the Run() goroutine
	go registry.Run()

	// Create unique clients for registration and unregistration
	registerClients := make([]*Client, 20)
	unregisterClients := make([]*Client, 20)
	for i := 0; i < 20; i++ {
		registerClients[i] = &Client{
			ID:         "reg-client-" + string(rune('0'+i)),
			WayfarerID: "user-" + string(rune('0'+i%5)), // 5 users
			Conn:       &mockConn{},
			Send:       make(chan []byte, 10),
		}
		unregisterClients[i] = &Client{
			ID:         "unreg-client-" + string(rune('0'+i)),
			WayfarerID: "user-" + string(rune('0'+i%5)), // 5 users
			Conn:       &mockConn{},
			Send:       make(chan []byte, 10),
		}
	}

	// Concurrently register and unregister different clients
	var wg sync.WaitGroup

	// Register loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			for _, c := range registerClients {
				registry.Register(c)
				time.Sleep(time.Microsecond)
			}
		}
	}()

	// Unregister loop (using different clients)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			for _, c := range unregisterClients {
				registry.Unregister(c)
				time.Sleep(time.Microsecond)
			}
		}
	}()

	// Concurrent reads
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(userIdx int) {
			defer wg.Done()
			wayfarerID := "user-" + string(rune('0'+userIdx))
			for i := 0; i < 100; i++ {
				_ = registry.GetClients(wayfarerID)
				_ = registry.Count()
				_ = registry.IsOnline(wayfarerID)
				// Prevent optimization
				_ = time.Now()
			}
		}(i)
	}

	wg.Wait()
}

// TestClientRegistryMutexWorksCorrectly tests that the mutex properly serializes access.
func TestClientRegistryMutexWorksCorrectly(t *testing.T) {
	registry := NewClientRegistry()
	go registry.Run()

	// Register a client
	client := &Client{
		ID:         "test-client",
		WayfarerID: "test-user",
		Conn:       &mockConn{},
		Send:       make(chan []byte, 10),
	}
	registry.Register(client)

	// Verify client is registered
	if !registry.IsOnline("test-user") {
		t.Error("expected client to be online")
	}

	clients := registry.GetClients("test-user")
	if len(clients) != 1 {
		t.Errorf("expected 1 client, got %d", len(clients))
	}

	if registry.Count() != 1 {
		t.Errorf("expected count 1, got %d", registry.Count())
	}

	// Unregister
	registry.Unregister(client)
	time.Sleep(10 * time.Millisecond) // Give time for unregister to process

	// Verify client is unregistered
	if registry.IsOnline("test-user") {
		t.Error("expected client to be offline after unregister")
	}

	if registry.Count() != 0 {
		t.Errorf("expected count 0, got %d", registry.Count())
	}
}
