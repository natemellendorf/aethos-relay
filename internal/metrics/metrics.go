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

	// MessagesDroppedTotal tracks total messages dropped due to backpressure.
	MessagesDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "messages_dropped_total",
		Help: "Total messages dropped due to full send channel",
	})

	// RelayPublishSuccessTotal tracks successful federation publishes.
	RelayPublishSuccessTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "relay_publish_success_total",
		Help: "Total successful federation publishes",
	})

	// RelayPublishFailureTotal tracks failed federation publishes.
	RelayPublishFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "relay_publish_failure_total",
		Help: "Total failed federation publishes",
	})

	// PublishWidthCurrent tracks the current adaptive publish width.
	PublishWidthCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "publish_width_current",
		Help: "Current adaptive publish width",
	})

	// RelayScoreGauge tracks relay scores.
	RelayScoreGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "relay_score",
		Help: "Current score for a relay",
	}, []string{"relay_id"})

	// DescriptorMetrics tracks descriptor-related metrics.
	DescriptorMetrics = &DescriptorMetricsGroup{}
)

// DescriptorMetricsGroup holds descriptor-related metrics.
type DescriptorMetricsGroup struct {
	DescriptorsReceived           prometheus.Counter
	DescriptorsAccepted           prometheus.Counter
	DescriptorsRejectedValidation prometheus.Counter
	DescriptorsRejectedRateLimit  prometheus.Counter
	DescriptorsExpired            prometheus.Counter
	DescriptorsRegistrySize       prometheus.Gauge
}

func init() {
	DescriptorMetrics.DescriptorsReceived = promauto.NewCounter(prometheus.CounterOpts{
		Name: "descriptors_received_total",
		Help: "Total relay descriptors received",
	})

	DescriptorMetrics.DescriptorsAccepted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "descriptors_accepted_total",
		Help: "Total relay descriptors accepted",
	})

	DescriptorMetrics.DescriptorsRejectedValidation = promauto.NewCounter(prometheus.CounterOpts{
		Name: "descriptors_rejected_validation_total",
		Help: "Total relay descriptors rejected due to validation",
	})

	DescriptorMetrics.DescriptorsRejectedRateLimit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "descriptors_rejected_rate_limit_total",
		Help: "Total relay descriptors rejected due to rate limiting",
	})

	DescriptorMetrics.DescriptorsExpired = promauto.NewCounter(prometheus.CounterOpts{
		Name: "descriptors_expired_total",
		Help: "Total relay descriptors expired and removed",
	})

	DescriptorMetrics.DescriptorsRegistrySize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "descriptors_registry_size",
		Help: "Current number of descriptors in registry",
	})
}

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

// IncrementDropped increments the dropped messages counter.
func IncrementDropped() {
	MessagesDroppedTotal.Inc()
}

// Descriptor metrics helpers
func IncrementDescriptorsReceived() {
	DescriptorMetrics.DescriptorsReceived.Inc()
}

func IncrementDescriptorsAccepted() {
	DescriptorMetrics.DescriptorsAccepted.Inc()
}

func IncrementDescriptorsRejectedValidation() {
	DescriptorMetrics.DescriptorsRejectedValidation.Inc()
}

func IncrementDescriptorsRejectedRateLimit() {
	DescriptorMetrics.DescriptorsRejectedRateLimit.Inc()
}

func IncrementDescriptorsExpired() {
	DescriptorMetrics.DescriptorsExpired.Inc()
}

func SetDescriptorsRegistrySize(size int) {
	DescriptorMetrics.DescriptorsRegistrySize.Set(float64(size))
}

// IncrementPublishSuccess increments the successful publish counter.
func IncrementPublishSuccess() {
	RelayPublishSuccessTotal.Inc()
}

// IncrementPublishFailure increments the failed publish counter.
func IncrementPublishFailure() {
	RelayPublishFailureTotal.Inc()
}

// SetPublishWidth sets the current publish width gauge.
func SetPublishWidth(width int) {
	PublishWidthCurrent.Set(float64(width))
}

// SetRelayScore sets the score for a specific relay.
func SetRelayScore(relayID string, score float64) {
	RelayScoreGauge.WithLabelValues(relayID).Set(score)
}

// RemoveRelayScore removes a relay score metric.
func RemoveRelayScore(relayID string) {
	RelayScoreGauge.DeleteLabelValues(relayID)
}
