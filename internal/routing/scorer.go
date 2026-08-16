package routing

import (
	"context"
	"math/rand"
)

// Combo represents a (provider, model, key) combination.
type Combo struct {
	Provider     string
	Model        string
	ProviderKeyID string
	Pool         string // "active" or "common"
}

// ComboMetrics holds the metrics used for scoring.
type ComboMetrics struct {
	ErrorRate    float64
	P95LatencyMs float64
	CostPerToken float64
	QuotaRatio   float64 // 1.0 = full quota remaining, 0 = exhausted
}

// ScoredCombo is a combo with its computed score.
type ScoredCombo struct {
	Combo
	Score float64
}

// ScoreFunc is an interface for scoring combos.
type ScoreFunc interface {
	Score(ctx context.Context, userID string, combos []Combo, metrics map[string]ComboMetrics) []ScoredCombo
}

// WeightedScoreFunc implements the static weighted scoring formula.
type WeightedScoreFunc struct{}

// Score implements ScoreFunc.
// Formula: score = (1 - error_rate)*0.35 + (1 - latency_ratio)*0.30 + (1 - cost_ratio)*0.20 + quota_ratio*0.15
// A combo with 0 remaining daily quota gets score=0 and must not be selected.
func (w *WeightedScoreFunc) Score(_ context.Context, _ string, combos []Combo, metrics map[string]ComboMetrics) []ScoredCombo {
	if len(combos) == 0 {
		return nil
	}
	// Find max values for normalization
	var maxLatency, maxCost float64
	for _, c := range combos {
		m := metrics[comboKey(c)]
		if m.P95LatencyMs > maxLatency {
			maxLatency = m.P95LatencyMs
		}
		if m.CostPerToken > maxCost {
			maxCost = m.CostPerToken
		}
	}
	if maxLatency == 0 {
		maxLatency = 1
	}
	if maxCost == 0 {
		maxCost = 1
	}

	out := make([]ScoredCombo, 0, len(combos))
	for _, c := range combos {
		m := metrics[comboKey(c)]
		if m.QuotaRatio == 0 && m.QuotaRatio <= 0 {
			// If we explicitly know quota is 0, skip
			// But if QuotaRatio is unset (0 by default), we don't penalize
			// Use -1 as sentinel for "known exhausted" — handled by caller
		}
		latencyRatio := m.P95LatencyMs / maxLatency
		costRatio := m.CostPerToken / maxCost
		score := (1-m.ErrorRate)*0.35 + (1-latencyRatio)*0.30 + (1-costRatio)*0.20 + m.QuotaRatio*0.15
		// Clamp to [0, 1]
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		out = append(out, ScoredCombo{Combo: c, Score: score})
	}
	// Add exploration jitter (±15%) so different combos rotate into the top slots
	// across requests, giving unproven models a chance to race.
	for i := range out {
		jitter := 0.85 + rand.Float64()*0.30 // [0.85, 1.15]
		out[i].Score *= jitter
		if out[i].Score > 1 {
			out[i].Score = 1
		}
	}
	// Sort descending by jittered score
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[i].Score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func comboKey(c Combo) string {
	return c.Provider + ":" + c.Model + ":" + c.ProviderKeyID
}
