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
	boolFlags := map[string]struct{}{
		"log-json":                 {},
		"dev-mode":                 {},
		"auto-peer-discovery":      {},
		"federation-pad-enabled":   {},
		"federation-cover-enabled": {},
	}
	os.Args = normalizeBoolFlagArgs(os.Args, boolFlags)

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
	envelopeStorePath := flag.String("envelope-store-path", "", "Path to envelope store (defaults to store-path + '.envelopes')")

	// Abuse resistance flags
	maxEnvelopeSize := flag.Int("max-envelope-size", 65536, "Maximum envelope payload size in bytes")
	maxFederationPeers := flag.Int("max-federation-peers", 50, "Maximum number of federation peers")
	rateLimitPerPeer := flag.Int("rate-limit-per-peer", 100, "Rate limit per peer (requests per minute)")

	// Relay discovery flags
	autoPeerDiscovery := flag.Bool("auto-peer-discovery", false, "Enable automatic peer discovery via gossip (default false)")
	descriptorStorePath := flag.String("descriptor-store-path", "", "Path to descriptor bbolt database (defaults to store-path + '.descriptors')")

	// TAR (Traffic Analysis Resistance) flags
	federationTopK := flag.Int("federation-topk", 2, "Number of top peers to forward to (default 2)")
	federationExploreProb := flag.Float64("federation-explore-prob", 0.1, "Probability of exploring a non-topK peer (default 0.1)")
	federationBatchInterval := flag.Duration("federation-batch-interval", 500*time.Millisecond, "Batching interval (default 500ms)")
	federationBatchJitter := flag.Duration("federation-batch-jitter", 250*time.Millisecond, "Batching jitter range (default 250ms)")
	federationBatchMax := flag.Int("federation-batch-max", 10, "Max frames per batch (default 10)")
	federationPadBuckets := flag.String("federation-pad-buckets", "1024,4096,16384,65536", "Padding bucket sizes (comma-separated)")
	federationPadEnabled := flag.Bool("federation-pad-enabled", false, "Enable payload padding (default false)")
	federationCoverEnabled := flag.Bool("federation-cover-enabled", false, "Enable cover frames (default false)")
	federationCoverMax := flag.Int("federation-cover-max", 3, "Max cover frames when queue empty (default 3)")

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
	log.Printf("Max envelope size: %d bytes", *maxEnvelopeSize)
	log.Printf("Max federation peers: %d", *maxFederationPeers)
	log.Printf("Rate limit per peer: %d req/min", *rateLimitPerPeer)

	// Parse padding buckets
	var padBuckets []int
	if *federationPadBuckets != "" {
		for _, b := range strings.Split(*federationPadBuckets, ",") {
			var bucket int
			if _, err := fmt.Sscanf(strings.TrimSpace(b), "%d", &bucket); err == nil && bucket > 0 {
				padBuckets = append(padBuckets, bucket)
			}
		}
	}
	if len(padBuckets) == 0 {
		padBuckets = []int{1024, 4096, 16384, 65536}
	}

	// Initialize TAR config
	tarConfig := &federation.TARConfig{
		BatchInterval:  *federationBatchInterval,
		BatchJitter:    *federationBatchJitter,
		BatchMax:       *federationBatchMax,
		PaddingEnabled: *federationPadEnabled,
		PadBuckets:     padBuckets,
		CoverEnabled:   *federationCoverEnabled,
		CoverMax:       *federationCoverMax,
	}
	tarConfig.Validate()

	log.Printf("TAR config: batch_interval=%v, batch_jitter=%v, batch_max=%d", tarConfig.BatchInterval, tarConfig.BatchJitter, tarConfig.BatchMax)
	log.Printf("TAR padding: enabled=%v, buckets=%v", tarConfig.PaddingEnabled, tarConfig.PadBuckets)
	log.Printf("TAR cover: enabled=%v, max=%d", tarConfig.CoverEnabled, tarConfig.CoverMax)

	// Initialize forwarding config
	forwardingConfig := &federation.ForwardingConfig{
		TopK:        *federationTopK,
		ExploreProb: *federationExploreProb,
	}
	forwardingConfig.Validate()

	log.Printf("Forwarding: topk=%d, explore_prob=%.2f", forwardingConfig.TopK, forwardingConfig.ExploreProb)

	// Determine envelope store path
	envStorePath := *envelopeStorePath
	if envStorePath == "" {
		envStorePath = *storePath + ".envelopes"
	}
	log.Printf("Envelope store path: %s", envStorePath)

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

	// Initialize envelope store for federation
	envelopeStore := store.NewBBoltEnvelopeStore(envStorePath)
	if err := envelopeStore.Open(); err != nil {
		log.Fatalf("Failed to open envelope store: %v", err)
	}
	defer envelopeStore.Close()

	log.Println("Envelope store opened successfully")

	// Initialize TTL sweeper (needed for descriptor store)
	maxTTL := time.Duration(*maxTTLSecs) * time.Second

	// Initialize envelope sweeper
	envelopeSweeperConfig := store.EnvelopeTTLDefaultConfig(maxTTL)
	envelopeSweeper := store.NewEnvelopeSweeper(envelopeStore, envelopeSweeperConfig)
	envelopeSweeper.SetExpiredCounter(metrics.IncrementEnvelopesExpired)

	// Create context for background tasks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start envelope sweeper in background
	go envelopeSweeper.Start(ctx)
	log.Println("Envelope TTL sweeper started")

	// Initialize descriptor store for relay discovery (if enabled)
	var descriptorStore store.DescriptorStore
	var descriptorSweeper *store.DescriptorSweeper
	var gossipEngine *gossip.GossipEngine

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

	// Initialize federation peer manager with TAR and forwarding config
	federationManager := federation.NewPeerManagerWithConfig(*relayID, bbstore, clients, maxTTL, tarConfig, forwardingConfig)
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

	// Federation WebSocket endpoint with rate limiting
	federationSem := make(chan struct{}, *maxFederationConns)
	rateLimiter := federation.NewRateLimiter(*rateLimitPerPeer, time.Minute)

	// Start rate limiter cleanup
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rateLimiter.Cleanup()
			}
		}
	}()

	mux.HandleFunc("/federation/ws", func(w http.ResponseWriter, r *http.Request) {
		// Check peer count limit
		if federationManager.GetPeerCount() >= *maxFederationPeers {
			http.Error(w, "Maximum federation peers reached", http.StatusServiceUnavailable)
			return
		}

		// Rate limit by remote addr
		peerID := r.RemoteAddr
		if !rateLimiter.Allow(peerID) {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

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

	// Register federation peers endpoint
	federationPeersHandler := api.NewFederationPeersHandler(federationManager)
	mux.HandleFunc("/federation/peers", federationPeersHandler.ServeHTTP)
	log.Println("Federation peers endpoint registered at /federation/peers")

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

func normalizeBoolFlagArgs(args []string, boolFlags map[string]struct{}) []string {
	if len(args) <= 1 {
		return args
	}

	normalized := make([]string, 0, len(args))
	normalized = append(normalized, args[0])

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			normalized = append(normalized, args[i:]...)
			break
		}

		if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
			flagName := strings.TrimLeft(arg, "-")
			if _, ok := boolFlags[flagName]; ok && i+1 < len(args) {
				next := strings.ToLower(args[i+1])
				if next == "true" || next == "false" {
					normalized = append(normalized, arg+"="+next)
					i++
					continue
				}
			}
		}

		normalized = append(normalized, arg)
	}

	return normalized
}
