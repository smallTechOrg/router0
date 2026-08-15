package handlers

import (
	"net/http"
	"strconv"
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
	active, common, err := h.routingPool.LoadPool(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load pool")
		return
	}

	type comboJSON struct {
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		ProviderKeyID string  `json:"provider_key_id"`
		Score         float64 `json:"score"`
		Pool          string  `json:"pool"`
	}

	toJSON := func(combos []routing.Combo) []comboJSON {
		out := make([]comboJSON, 0, len(combos))
		for _, c := range combos {
			out = append(out, comboJSON{
				Provider:      c.Provider,
				Model:         c.Model,
				ProviderKeyID: c.ProviderKeyID,
				Score:         0.5,
				Pool:          c.Pool,
			})
		}
		return out
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active": toJSON(active),
		"common": toJSON(common),
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
		`SELECT id, COALESCE(zer0_request_id,''), provider, model, COALESCE(pool,'active'), 
		 input_tokens, output_tokens, latency_ms, 
		 COALESCE(estimated_cost_usd, cost_usd, 0), status, COALESCE(retry_count,0), created_at
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
	}

	var requests []reqJSON
	for rows.Next() {
		var req reqJSON
		var createdAt time.Time
		if err := rows.Scan(&req.ID, &req.Zer0RequestID, &req.Provider, &req.Model, &req.Pool,
			&req.InputTokens, &req.OutputTokens, &req.LatencyMs, &req.EstimatedCostUSD,
			&req.Status, &req.RetryCount, &createdAt); err != nil {
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
