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

	// KeySkipsTotal: candidates skipped without a call, by key-state reason
	// (rate_limited | credits_exhausted | budget_exhausted).
	KeySkipsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_key_skips_total",
		Help: "Candidates skipped without an upstream call, by provider and key-state reason",
	}, []string{"provider", "reason"})

	// SpendUSDTotal: estimated cost of successful requests (what budgets count).
	SpendUSDTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_spend_usd_total",
		Help: "Estimated USD cost of successful requests, by winning provider",
	}, []string{"provider"})

	// BalancePollsTotal: per-key balance polls by provider and result
	// (ok | exhausted | error).
	BalancePollsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_balance_polls_total",
		Help: "Provider balance polls per key, by provider and result",
	}, []string{"provider", "result"})

	// SpendReconcileTotal: hourly spend reconciliations by status (ok | error).
	SpendReconcileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router0_spend_reconcile_total",
		Help: "Month-to-date spend reconciliations from the request log, by status",
	}, []string{"status"})

	// SpendReconcileCorrectedKeys: key counters inserted or corrected by reconciles.
	SpendReconcileCorrectedKeys = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router0_spend_reconcile_corrected_keys_total",
		Help: "Per-key spend counters inserted or corrected by reconciliation",
	})
)
