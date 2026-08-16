package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/routing"
)

type PoolHandler struct {
	pgPool      *pgxpool.Pool
	routingPool *routing.Pool
}

func NewPoolHandler(pg *pgxpool.Pool, routingPool *routing.Pool) *PoolHandler {
	return &PoolHandler{pgPool: pg, routingPool: routingPool}
}

func (h *PoolHandler) GetPool(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())

	// Load scored combos from Redis directly so scores are real
	ctx := r.Context()
	type comboJSON struct {
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		ProviderKeyID string  `json:"provider_key_id"`
		Score         float64 `json:"score"`
		Pool          string  `json:"pool"`
	}

	loadScoredFromRedis := func(redisKey, poolLabel string) []comboJSON {
		members, err := h.routingPool.Redis().ZRevRangeWithScores(ctx, redisKey, 0, -1).Result()
		if err != nil {
			return []comboJSON{}
		}
		out := make([]comboJSON, 0, len(members))
		for _, m := range members {
			if m.Score == 0 {
				continue
			}
			memberStr := m.Member.(string)
			lastColon := strings.LastIndex(memberStr, ":")
			if lastColon <= 0 {
				continue
			}
			keyID := memberStr[lastColon+1:]
			rest := memberStr[:lastColon]
			firstColon := strings.Index(rest, ":")
			if firstColon <= 0 {
				continue
			}
			out = append(out, comboJSON{
				Provider:      rest[:firstColon],
				Model:         rest[firstColon+1:],
				ProviderKeyID: keyID,
				Score:         m.Score,
				Pool:          poolLabel,
			})
		}
		return out
	}

	pro := loadScoredFromRedis("pool:"+userID+":pro", "pro")
	flash := loadScoredFromRedis("pool:"+userID+":flash", "flash")
	flashLite := loadScoredFromRedis("pool:"+userID+":flash-lite", "flash-lite")

	// Per-model analytics from request_metadata (last 10 minutes only)
	statsRows, _ := h.pgPool.Query(ctx, `
		SELECT provider, model,
			COUNT(*) AS total_reqs,
			COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '1 hour') AS reqs_1h,
			COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '24 hours') AS reqs_24h,
			ROUND(AVG(latency_ms)) AS avg_latency_ms,
			ROUND(AVG(latency_ms) FILTER (WHERE created_at > NOW() - INTERVAL '1 hour')) AS avg_latency_1h,
			SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END) AS errors,
			SUM(input_tokens + output_tokens) AS total_tokens,
			MAX(created_at) AS last_seen
		FROM request_metadata
		WHERE user_id = $1 AND pool != 'perf_test' AND created_at > NOW() - INTERVAL '10 minutes'
		GROUP BY provider, model`, userID)

	type modelStats struct {
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		TotalReqs     int64   `json:"total_reqs"`
		Reqs1h        int64   `json:"reqs_1h"`
		Reqs24h       int64   `json:"reqs_24h"`
		AvgLatencyMs  *int64  `json:"avg_latency_ms"`
		AvgLatency1h  *int64  `json:"avg_latency_1h"`
		Errors        int64   `json:"errors"`
		TotalTokens   int64   `json:"total_tokens"`
		LastSeen      string  `json:"last_seen"`
	}
	var stats []modelStats
	if statsRows != nil {
		defer statsRows.Close()
		for statsRows.Next() {
			var s modelStats
			var lastSeen time.Time
			if statsRows.Scan(&s.Provider, &s.Model, &s.TotalReqs, &s.Reqs1h, &s.Reqs24h,
				&s.AvgLatencyMs, &s.AvgLatency1h, &s.Errors, &s.TotalTokens, &lastSeen) == nil {
				s.LastSeen = lastSeen.Format(time.RFC3339)
				stats = append(stats, s)
			}
		}
	}
	if stats == nil {
		stats = []modelStats{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pro":         pro,
		"flash":       flash,
		"flash_lite":  flashLite,
		"model_stats": stats,
	})
}


func (h *PoolHandler) GetRequests(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}

	rows, err := h.pgPool.Query(r.Context(),
		`SELECT id, COALESCE(zer0_request_id,''), provider, model, COALESCE(pool,'flash'),
		 input_tokens, output_tokens, latency_ms,
		 COALESCE(estimated_cost_usd, cost_usd, 0), status, COALESCE(retry_count,0), created_at,
		 COALESCE(error_class,''), COALESCE(error_message,'')
		 FROM request_metadata
		 WHERE user_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to fetch requests")
		return
	}
	defer rows.Close()

	type reqJSON struct {
		ID              string  `json:"id"`
		Zer0RequestID   string  `json:"zer0_request_id"`
		Provider        string  `json:"provider"`
		Model           string  `json:"model"`
		Pool            string  `json:"pool"`
		InputTokens     int     `json:"input_tokens"`
		OutputTokens    int     `json:"output_tokens"`
		LatencyMs       int64   `json:"latency_ms"`
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
		Status          string  `json:"status"`
		RetryCount      int     `json:"retry_count"`
		CreatedAt       string  `json:"created_at"`
		ErrorClass      string  `json:"error_class,omitempty"`
		ErrorMessage    string  `json:"error_message,omitempty"`
	}

	var requests []reqJSON
	for rows.Next() {
		var req reqJSON
		var createdAt time.Time
		if err := rows.Scan(&req.ID, &req.Zer0RequestID, &req.Provider, &req.Model, &req.Pool,
			&req.InputTokens, &req.OutputTokens, &req.LatencyMs, &req.EstimatedCostUSD,
			&req.Status, &req.RetryCount, &createdAt, &req.ErrorClass, &req.ErrorMessage); err != nil {
			continue
		}
		req.CreatedAt = createdAt.Format(time.RFC3339)
		requests = append(requests, req)
	}
	if requests == nil {
		requests = []reqJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": requests})
}

func (h *PoolHandler) GetCosts(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	now := time.Now().UTC()
	monthStr := now.Format("2006-01")

	rows, err := h.pgPool.Query(r.Context(),
		`SELECT provider, COUNT(*) as request_count, 
		 SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens,
		 SUM(COALESCE(estimated_cost_usd, cost_usd, 0)) as total_cost
		 FROM request_metadata
		 WHERE user_id = $1 AND date_trunc('month', created_at) = date_trunc('month', NOW())
		 GROUP BY provider`,
		userID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to fetch costs")
		return
	}
	defer rows.Close()

	type costJSON struct {
		Provider         string  `json:"provider"`
		RequestCount     int64   `json:"request_count"`
		InputTokens      int64   `json:"input_tokens"`
		OutputTokens     int64   `json:"output_tokens"`
		EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	}

	var byProvider []costJSON
	var total float64
	for rows.Next() {
		var c costJSON
		if err := rows.Scan(&c.Provider, &c.RequestCount, &c.InputTokens, &c.OutputTokens, &c.EstimatedCostUSD); err != nil {
			continue
		}
		total += c.EstimatedCostUSD
		byProvider = append(byProvider, c)
	}
	if byProvider == nil {
		byProvider = []costJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"month":                    monthStr,
		"by_provider":              byProvider,
		"total_estimated_cost_usd": total,
	})
}

func (h *PoolHandler) GetAttempts(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())

	rows, err := h.pgPool.Query(r.Context(), `
		SELECT provider, model,
			COUNT(*) AS total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS successes,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS errors,
			ROUND(AVG(latency_ms)) AS avg_latency_ms,
			mode() WITHIN GROUP (ORDER BY error_class) FILTER (WHERE error_class IS NOT NULL) AS top_error_class,
			MAX(created_at) AS last_seen
		FROM request_attempts
		WHERE user_id = $1 AND created_at > NOW() - INTERVAL '10 minutes'
		GROUP BY provider, model
		ORDER BY errors DESC, total DESC`, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to fetch attempts")
		return
	}
	defer rows.Close()

	type attemptStat struct {
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		Total         int64  `json:"total"`
		Successes     int64  `json:"successes"`
		Errors        int64  `json:"errors"`
		AvgLatencyMs  *int64 `json:"avg_latency_ms"`
		TopErrorClass *string `json:"top_error_class"`
		LastSeen      string `json:"last_seen"`
	}

	var stats []attemptStat
	for rows.Next() {
		var s attemptStat
		var lastSeen time.Time
		if rows.Scan(&s.Provider, &s.Model, &s.Total, &s.Successes, &s.Errors,
			&s.AvgLatencyMs, &s.TopErrorClass, &lastSeen) == nil {
			s.LastSeen = lastSeen.Format(time.RFC3339)
			stats = append(stats, s)
		}
	}
	if stats == nil {
		stats = []attemptStat{}
	}

	// Recent individual errors for detail view
	errRows, err := h.pgPool.Query(r.Context(), `
		SELECT zer0_request_id, provider, model, error_class, error_message, http_status, latency_ms, created_at
		FROM request_attempts
		WHERE user_id = $1 AND status = 'error' AND created_at > NOW() - INTERVAL '10 minutes'
		ORDER BY created_at DESC
		LIMIT 50`, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"attempt_stats": stats, "recent_errors": []struct{}{}})
		return
	}
	defer errRows.Close()

	type recentError struct {
		Zer0RequestID string  `json:"zer0_request_id"`
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		ErrorClass    *string `json:"error_class"`
		ErrorMessage  *string `json:"error_message"`
		HTTPStatus    *int    `json:"http_status"`
		LatencyMs     int64   `json:"latency_ms"`
		CreatedAt     string  `json:"created_at"`
	}
	var recentErrors []recentError
	for errRows.Next() {
		var e recentError
		var createdAt time.Time
		if errRows.Scan(&e.Zer0RequestID, &e.Provider, &e.Model, &e.ErrorClass, &e.ErrorMessage,
			&e.HTTPStatus, &e.LatencyMs, &createdAt) == nil {
			e.CreatedAt = createdAt.Format(time.RFC3339)
			recentErrors = append(recentErrors, e)
		}
	}
	if recentErrors == nil {
		recentErrors = []recentError{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"attempt_stats": stats,
		"recent_errors": recentErrors,
	})
}
