package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricNamesRenamed(t *testing.T) {
	RequestsTotal.WithLabelValues("groq", "success", "ok").Inc()
	RequestDuration.WithLabelValues("groq", "false").Observe(0.2)
	AttemptsTotal.WithLabelValues("groq", "error", "rate_limit").Inc()
	KeyStateMarksTotal.WithLabelValues("groq", "rate_limited").Inc()
	CatalogRefreshTotal.WithLabelValues("groq", "ok").Inc()
	TokenAuthFailuresTotal.WithLabelValues("unknown").Inc()
	MetadataDroppedTotal.Inc()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "zer0_") {
			t.Fatalf("legacy metric %s", f.GetName())
		}
		seen[f.GetName()] = true
	}
	for _, n := range []string{"router0_requests_total", "router0_request_duration_seconds", "router0_attempts_total",
		"router0_key_state_marks_total", "router0_catalog_refresh_total", "router0_token_auth_failures_total", "router0_metadata_dropped_total"} {
		if !seen[n] {
			t.Errorf("missing %s", n)
		}
	}
}
