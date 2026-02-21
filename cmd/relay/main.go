package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/natemellendorf/aethos-relay/internal/api"
	"github.com/natemellendorf/aethos-relay/internal/federation"
	"github.com/natemellendorf/aethos-relay/internal/gossip"
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
	allowedOrigins := flag.String("allowed-origins", "", "Comma-separated list of allowed WebSocket origins (e.g., 'https://app.aethos.io,https://aethos.app')")
	devMode := flag.Bool("dev-mode", false, "Enable development mode (allows all origins, for local development only)")

	// Federation flags
	relayID := flag.String("relay-id", "", "Unique relay ID (auto-generated if not provided)")
	peerURLs := flag.String("peer", "", "Comma-separated list of peer relay WebSocket URLs")
	maxFederationConns := flag.Int("max-federation-conns", 100, "Maximum concurrent inbound federation connections")

	// Relay discovery flags
	autoPeerDiscovery := flag.Bool("auto-peer-discovery", false, "Enable automatic peer discovery via gossip (default false)")
	descriptorStorePath := flag.String("descriptor-store-path", "", "Path to descriptor bbolt database (defaults to store-path + '.descriptors')")

	flag.Parse()

	if *logJSON {
		// For now, just note that JSON logging would require additional setup
		log.SetFlags(0)
		log.SetOutput(os.Stderr)
	}

	// Generate relay ID if not provided
	if *relayID == "" {
		*relayID = uuid.New().String()
	}

	log.Printf("Starting aethos-relay server...")
	log.Printf("Relay ID: %s", *relayID)
	log.Printf("WebSocket addr: %s", *wsAddr)
	log.Printf("HTTP addr: %s", *httpAddr)
	log.Printf("Store path: %s", *storePath)
	log.Printf("Sweep interval: %s", *sweepInterval)
	log.Printf("Max TTL: %d seconds", *maxTTLSecs)
	log.Printf("Allowed origins: %s", *allowedOrigins)
	log.Printf("Dev mode: %v", *devMode)
	log.Printf("Auto peer discovery: %v", *autoPeerDiscovery)

	// Determine descriptor store path
	descStorePath := *descriptorStorePath
	if descStorePath == "" {
		descStorePath = *storePath + ".descriptors"
	}
	log.Printf("Descriptor store path: %s", descStorePath)

	// Initialize store
	bbstore := store.NewBBoltStore(*storePath)
	if err := bbstore.Open(); err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}
	defer bbstore.Close()

	log.Println("Store opened successfully")

	// Initialize TTL sweeper (needed for descriptor store)
	maxTTL := time.Duration(*maxTTLSecs) * time.Second

	// Initialize descriptor store for relay discovery (if enabled)
	var descriptorStore store.DescriptorStore
	var descriptorSweeper *store.DescriptorSweeper
	var gossipEngine *gossip.GossipEngine

	// Create context for background tasks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *autoPeerDiscovery {
		descriptorStore = store.NewBBoltDescriptorStore(descStorePath)
		if err := descriptorStore.Open(); err != nil {
			log.Fatalf("Failed to open descriptor store: %v", err)
		}
		defer descriptorStore.Close()
		log.Println("Descriptor store opened successfully")

		// Initialize descriptor TTL sweeper
		descriptorSweeper = store.NewDescriptorSweeper(descriptorStore, *sweepInterval, maxTTL)
		descriptorSweeper.SetExpiredCounter(metrics.IncrementDescriptorsExpired)

		// Start descriptor sweeper in background
		go descriptorSweeper.Start(ctx)

		log.Println("Descriptor TTL sweeper started")

		// Initialize gossip engine
		gossipEngine = gossip.NewGossipEngine(descriptorStore, *relayID, *autoPeerDiscovery)
		go gossipEngine.Run(ctx)

		log.Println("Gossip engine started")
	}

	sweeper := store.NewTTLSweeper(bbstore, *sweepInterval, maxTTL)

	log.Println("TTL sweeper started")

	// Initialize client registry
	clients := model.NewClientRegistry()
	go clients.Run()

	// Initialize federation peer manager
	federationManager := federation.NewPeerManager(*relayID, bbstore, clients, maxTTL)
	go federationManager.Run()

	log.Println("Federation peer manager started")

	// Connect to configured peers
	if *peerURLs != "" {
		peerList := strings.Split(*peerURLs, ",")
		for _, peerURL := range peerList {
			peerURL = strings.TrimSpace(peerURL)
			if peerURL != "" {
				log.Printf("Connecting to peer: %s", peerURL)
				federationManager.AddPeerURL(peerURL)
			}
		}
	}

	// Initialize handlers
	wsHandler := api.NewWSHandler(bbstore, clients, maxTTL, *allowedOrigins, *devMode)
	wsHandler.SetFederationManager(federationManager)
	httpHandler := api.NewHTTPHandler(bbstore, sweeper, *maxTTLSecs)

	// Set up HTTP server with WebSocket and API handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.HandleWebSocket)

	// Limit concurrent inbound federation connections to prevent resource exhaustion.
	federationSem := make(chan struct{}, *maxFederationConns)
	mux.HandleFunc("/federation", func(w http.ResponseWriter, r *http.Request) {
		select {
		case federationSem <- struct{}{}:
			defer func() { <-federationSem }()
			federationManager.HandleInboundPeer(w, r)
		default:
			http.Error(w, "Too many concurrent federation connections", http.StatusServiceUnavailable)
		}
	})

	// Register relay descriptor endpoint if auto-peer-discovery is enabled
	if *autoPeerDiscovery && descriptorStore != nil {
		descriptorHandler := api.NewRelayDescriptorHandler(descriptorStore, *autoPeerDiscovery)
		mux.HandleFunc("/relay/descriptors", descriptorHandler.ServeHTTP)
		log.Println("Relay descriptor endpoint registered at /relay/descriptors")
	}

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
	var wsServer *http.Server
	if *wsAddr != *httpAddr {
		log.Printf("WebSocket server listening on %s", *wsAddr)
		wsServer = &http.Server{
			Addr:    *wsAddr,
			Handler: mux,
		}
		go func() {
			if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("WebSocket server error: %v", err)
			}
		}()
	}

	log.Println("Federation peer service ready")

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down...")
	cancel()
	sweeper.Stop()
	federationManager.Stop()

	// Stop descriptor components if enabled
	if descriptorSweeper != nil {
		descriptorSweeper.Stop()
	}
	if gossipEngine != nil {
		gossipEngine.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// Shutdown WebSocket server first if separate
	if wsServer != nil {
		if err := wsServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("WebSocket server shutdown error: %v", err)
		}
	}

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("Server stopped")
	fmt.Println("aethos-relay stopped gracefully")
}
