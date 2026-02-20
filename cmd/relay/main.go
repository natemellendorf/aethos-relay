package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/natemellendorf/aethos-relay/internal/api"
	"github.com/natemellendorf/aethos-relay/internal/metrics"
	"github.com/natemellendorf/aethos-relay/internal/model"
	"github.com/natemellendorf/aethos-relay/internal/store"
)

func main() {
	// Parse flags
	wsAddr := flag.String("ws-addr", ":8080", "WebSocket server address")
	httpAddr := flag.String("http-addr", ":8081", "HTTP server address")
	storePath := flag.String("store-path", "./relay.db", "Path to bbolt database")
	sweepInterval := flag.Duration("sweep-interval", 30*time.Second, "TTL sweeper interval")
	maxTTLSecs := flag.Int("max-ttl-seconds", 604800, "Maximum TTL in seconds (default 7 days)")
	logJSON := flag.Bool("log-json", false, "JSON logging output")
	flag.Parse()

	if *logJSON {
		// For now, just note that JSON logging would require additional setup
		log.SetFlags(0)
		log.SetOutput(os.Stderr)
	}

	log.Printf("Starting aethos-relay server...")
	log.Printf("WebSocket addr: %s", *wsAddr)
	log.Printf("HTTP addr: %s", *httpAddr)
	log.Printf("Store path: %s", *storePath)
	log.Printf("Sweep interval: %s", *sweepInterval)
	log.Printf("Max TTL: %d seconds", *maxTTLSecs)

	// Initialize store
	bbstore := store.NewBBoltStore(*storePath)
	if err := bbstore.Open(); err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}
	defer bbstore.Close()

	log.Println("Store opened successfully")

	// Initialize TTL sweeper
	maxTTL := time.Duration(*maxTTLSecs) * time.Second
	sweeper := store.NewTTLSweeper(bbstore, *sweepInterval, maxTTL)
	sweeper.SetExpiredCounter(metrics.IncrementExpired)

	// Start sweeper in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sweeper.Start(ctx)

	log.Println("TTL sweeper started")

	// Initialize client registry
	clients := model.NewClientRegistry()
	go clients.Run()

	// Initialize handlers
	wsHandler := api.NewWSHandler(bbstore, clients, maxTTL)
	httpHandler := api.NewHTTPHandler(bbstore, sweeper, *maxTTLSecs)

	// Set up HTTP server with WebSocket and API handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.HandleWebSocket)
	mux.HandleFunc("/", httpHandler.ServeHTTP)

	httpServer := &http.Server{
		Addr:    *httpAddr,
		Handler: mux,
	}

	// Start HTTP server
	go func() {
		log.Printf("HTTP server listening on %s", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Also start WebSocket server on separate port if different
	if *wsAddr != *httpAddr {
		log.Printf("WebSocket server listening on %s", *wsAddr)
		wsServer := &http.Server{
			Addr:    *wsAddr,
			Handler: mux,
		}
		go func() {
			if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("WebSocket server error: %v", err)
			}
		}()
	}

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down...")
	cancel()
	sweeper.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("Server stopped")
	fmt.Println("aethos-relay stopped gracefully")
}
