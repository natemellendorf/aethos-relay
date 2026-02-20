package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ConnectionsCurrent tracks current WebSocket connections.
	ConnectionsCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "connections_current",
		Help: "Current number of WebSocket connections",
	})

	// MessagesReceivedTotal tracks total messages received.
	MessagesReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "messages_received_total",
		Help: "Total messages received",
	})

	// MessagesPersistedTotal tracks total messages persisted.
	MessagesPersistedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "messages_persisted_total",
		Help: "Total messages persisted to store",
	})

	// MessagesDeliveredTotal tracks total messages delivered.
	MessagesDeliveredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "messages_delivered_total",
		Help: "Total messages delivered to recipients",
	})

	// MessagesExpiredTotal tracks total messages expired.
	MessagesExpiredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "messages_expired_total",
		Help: "Total messages expired and removed",
	})

	// StoreErrorsTotal tracks total store errors.
	StoreErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "store_errors_total",
		Help: "Total store errors",
	})
)

// IncrementExpired increments the expired messages counter.
func IncrementExpired() {
	MessagesExpiredTotal.Inc()
}

// IncrementDelivered increments the delivered messages counter.
func IncrementDelivered() {
	MessagesDeliveredTotal.Inc()
}

// IncrementPersisted increments the persisted messages counter.
func IncrementPersisted() {
	MessagesPersistedTotal.Inc()
}

// IncrementReceived increments the received messages counter.
func IncrementReceived() {
	MessagesReceivedTotal.Inc()
}

// IncrementStoreErrors increments the store errors counter.
func IncrementStoreErrors() {
	StoreErrorsTotal.Inc()
}
