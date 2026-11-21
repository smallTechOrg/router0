// Package metrics defines the router0_* Prometheus metrics. Labels are bounded:
// there are no org, key, token or model labels (per-org breakdowns come from the
// request log).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RequestsTotal: provider is the winner or "none".
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_requests_total",
		Help: "Inference requests processed, by winning provider, status and error code",
	}, []string{"provider", "status", "code"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "router0_request_duration_seconds",
		Help:    "Inference request duration in seconds",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"provider", "stream"})

	AttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_attempts_total",
		Help: "Candidate attempts (success, error, skipped) by provider and error class",
	}, []string{"provider", "outcome", "class"})

	KeyStateMarksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_key_state_marks_total",
		Help: "Key state marks written after provider failures",
	}, []string{"provider", "reason"})

	CatalogRefreshTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_catalog_refresh_total",
		Help: "Catalog refreshes by provider and status",
	}, []string{"provider", "status"})

	TokenAuthFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_token_auth_failures_total",
		Help: "Service-token authentication failures (unknown, revoked, malformed)",
	}, []string{"reason"})

	MetadataDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router0_metadata_dropped_total",
		Help: "Request-log records dropped because the async buffer was full",
	})
)
