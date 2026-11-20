package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "zer0_requests_total",
		Help: "Total inference requests processed",
	}, []string{"provider", "model", "status"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "zer0_request_duration_seconds",
		Help:    "Request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "model"})

	PoolScore = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zer0_pool_score",
		Help: "Current routing score for a provider/model combo",
	}, []string{"user_id", "provider", "model"})

	ProviderErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "zer0_provider_errors_total",
		Help: "Total provider errors",
	}, []string{"provider", "model", "error_class"})
)
