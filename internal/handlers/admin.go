package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/models"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
)

type AdminHandler struct {
	pgPool      *pgxpool.Pool
	redis       *redis.Client
	routingPool *routing.Pool
	registry    *models.Registry
	discovery   *models.DiscoveryJob
}

func NewAdminHandler(pg *pgxpool.Pool, r *redis.Client, routingPool *routing.Pool, registry *models.Registry, discovery *models.DiscoveryJob) *AdminHandler {
	return &AdminHandler{pgPool: pg, redis: r, routingPool: routingPool, registry: registry, discovery: discovery}
}

func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	adminID := auth.UserIDFromCtx(r.Context())
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_request", "email is required")
		return
	}

	var token string
	var expiresAt time.Time
	err := h.pgPool.QueryRow(r.Context(),
		`INSERT INTO invites (email, token, created_by, invited_by, expires_at)
		 VALUES ($1, gen_random_uuid()::text, $2, $2, NOW() + INTERVAL '7 days')
		 RETURNING token, expires_at`,
		req.Email, adminID,
	).Scan(&token, &expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create invite")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token":      token,
		"email":      req.Email,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pgPool.Query(r.Context(),
		`SELECT id, email, role, status, created_at FROM users ORDER BY created_at DESC`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list users")
		return
	}
	defer rows.Close()

	type userJSON struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Role      string `json:"role"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	var users []userJSON
	for rows.Next() {
		var u userJSON
		var createdAt time.Time
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &createdAt); err != nil {
			continue
		}
		u.CreatedAt = createdAt.Format(time.RFC3339)
		users = append(users, u)
	}
	if users == nil {
		users = []userJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
}

// SetProviderKey stores an admin provider API key (used for model discovery).
func (h *AdminHandler) SetProviderKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Provider == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_request", "provider and key are required")
		return
	}
	if _, err := providers.Get(req.Provider); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_provider", "Unknown provider")
		return
	}
	ciphertext, nonce, err := keys.Encrypt(req.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "encrypt_failed", "Failed to encrypt key")
		return
	}
	if err := h.registry.SaveAdminKey(r.Context(), req.Provider, ciphertext, nonce, req.Label); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to save key")
		return
	}

	// Also register as a user-facing key for the admin so they can route through it immediately
	adminID := auth.UserIDFromCtx(r.Context())
	label := req.Label
	if label == "" {
		label = req.Provider + " admin key"
	}
	hint := req.Key
	if len(hint) > 8 {
		hint = hint[:8]
	}
	hint += "…"
	keyRepo := keys.NewRepository(h.pgPool)
	k, err := keyRepo.Create(r.Context(), adminID, req.Provider, label, hint, "", ciphertext, nonce)
	if err == nil {
		prov, _ := providers.Get(req.Provider)
		if prov != nil {
			for _, model := range prov.DefaultModels() {
				var paramB float64
				var ctxWindow int
				_ = h.pgPool.QueryRow(r.Context(),
					`SELECT COALESCE(parameter_count_b,0), context_window FROM provider_models WHERE provider=$1 AND model_id=$2`,
					req.Provider, model).Scan(&paramB, &ctxWindow)
				if paramB <= 0 {
					paramB = 50
					ctxWindow = 128000
				}
				h.routingPool.AddComboToAll(r.Context(), adminID, req.Provider, model, k.ID, paramB, ctxWindow)
			}
		}
	}

	// Trigger immediate discovery in background using a fresh context
	go func(provider, apiKey string) {
		h.discovery.RunForProvider(context.Background(), provider, apiKey)
	}(req.Provider, req.Key)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "provider": req.Provider})
}

// ListProviderKeys lists admin provider keys (no decrypted values).
func (h *AdminHandler) ListProviderKeys(w http.ResponseWriter, r *http.Request) {
	adminKeys, err := h.registry.ListAdminKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list keys")
		return
	}
	type keyJSON struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Status   string `json:"status"`
	}
	out := make([]keyJSON, 0, len(adminKeys))
	for _, k := range adminKeys {
		out = append(out, keyJSON{k.ID, k.Provider, k.Label, k.Status})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": out})
}

// ListModels returns all discovered models with scoring and rate limit metadata.
func (h *AdminHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")

	type rateLimitJSON struct {
		Plan     string `json:"plan"`
		LimitRPM *int   `json:"limit_rpm"`
		LimitTPM *int   `json:"limit_tpm"`
		LimitTPD *int   `json:"limit_tpd"`
		LimitRPD *int   `json:"limit_rpd"`
		Source   string `json:"source"`
	}
	type scoreBreakdown struct {
		Formula      string   `json:"formula"`
		ErrorRate    *float64 `json:"error_rate"`
		AvgLatencyMs *int     `json:"avg_latency_ms"`
		// Component weights
		ReliabilityWeight  float64  `json:"reliability_weight"`
		LatencyWeight      float64  `json:"latency_weight"`
		BaselineWeight     float64  `json:"baseline_weight"`
		AvailabilityWeight float64  `json:"availability_weight"`
		// Component values (nil if not enough data)
		ReliabilityScore  *float64 `json:"reliability_score"`
		LatencyScore      *float64 `json:"latency_score"`
		FinalScore        *float64 `json:"final_score"`
		DataSufficient    bool     `json:"data_sufficient"`
	}
	type modelJSON struct {
		Provider          string          `json:"provider"`
		ModelID           string          `json:"model_id"`
		DisplayName       string          `json:"display_name"`
		ContextWindow     int             `json:"context_window"`
		MaxOutputTokens   int             `json:"max_output_tokens"`
		InputCostPerMTok  float64         `json:"input_cost_per_mtok"`
		OutputCostPerMTok float64         `json:"output_cost_per_mtok"`
		ParameterCountB   *float64        `json:"parameter_count_b"`
		Architecture      *string         `json:"architecture"`
		Score             *float64        `json:"score"`
		AvgLatencyMs      *int            `json:"avg_latency_ms"`
		ErrorRate         *float64        `json:"error_rate"`
		ProbeStatus       string          `json:"probe_status"`
		// Historical stats from model_perf_stats
		TotalRequests  *int64   `json:"total_requests"`
		TotalErrors    *int64   `json:"total_errors"`
		P50LatencyMs   *int     `json:"p50_latency_ms"`
		P95LatencyMs   *int     `json:"p95_latency_ms"`
		P99LatencyMs   *int     `json:"p99_latency_ms"`
		MinLatencyMs   *int     `json:"min_latency_ms"`
		MaxLatencyMs   *int     `json:"max_latency_ms"`
		ScoreBreakdown    scoreBreakdown  `json:"score_breakdown"`
		RateLimits        []rateLimitJSON `json:"rate_limits"`
	}

	// Only show probe_ok models with context_window >= 100K.
	// probe_error and small-context models are excluded from routing and hidden from the registry.
	whereClause := "WHERE pm.is_available = true AND pm.probe_status = 'probe_ok' AND pm.parameter_count_b > 0"
	args := []interface{}{}
	if provider != "" {
		whereClause += " AND pm.provider = $1"
		args = append(args, provider)
	}

	rows, err := h.pgPool.Query(r.Context(), `
		WITH effective_limits AS (
			-- For each provider+model+plan combo, prefer observed over published.
			-- Merge: use observed values where available, fall back to published for missing fields.
			SELECT
				COALESCE(obs.provider, pub.provider) AS provider,
				COALESCE(obs.model_id, pub.model_id) AS model_id,
				CASE WHEN obs.provider IS NOT NULL THEN 'observed' ELSE pub.plan END AS plan,
				COALESCE(obs.limit_rpm, pub.limit_rpm) AS limit_rpm,
				COALESCE(obs.limit_tpm, pub.limit_tpm) AS limit_tpm,
				COALESCE(obs.limit_tpd, pub.limit_tpd) AS limit_tpd,
				pub.limit_rpd,
				CASE WHEN obs.provider IS NOT NULL THEN 'observed' ELSE pub.source END AS source
			FROM provider_rate_limits pub
			LEFT JOIN provider_rate_limits obs
				ON obs.provider = pub.provider
				AND obs.model_id = pub.model_id
				AND obs.plan = 'observed'
			WHERE pub.plan != 'observed'  -- start from published rows, overlay observed
			UNION ALL
			-- Also include pure observed rows (for models not in published seed)
			SELECT provider, model_id, 'observed' AS plan,
				limit_rpm, limit_tpm, limit_tpd, NULL AS limit_rpd, 'observed' AS source
			FROM provider_rate_limits
			WHERE plan = 'observed'
			  AND NOT EXISTS (
				SELECT 1 FROM provider_rate_limits p2
				WHERE p2.provider = provider_rate_limits.provider
				  AND p2.model_id = provider_rate_limits.model_id
				  AND p2.plan != 'observed'
			  )
		)
		SELECT
			pm.provider, pm.model_id, pm.display_name, pm.context_window, pm.max_output_tokens,
			pm.input_cost_per_mtok, pm.output_cost_per_mtok,
			pm.parameter_count_b, pm.architecture,
			pm.score, pm.avg_latency_ms, pm.error_rate, pm.probe_status,
			mps.total_requests, mps.total_errors,
			mps.p50_latency_ms, mps.p95_latency_ms, mps.p99_latency_ms,
			mps.min_latency_ms, mps.max_latency_ms,
			COALESCE(
				json_agg(json_build_object(
					'plan', el.plan,
					'limit_rpm', el.limit_rpm,
					'limit_tpm', el.limit_tpm,
					'limit_tpd', el.limit_tpd,
					'limit_rpd', el.limit_rpd,
					'source', el.source
				)) FILTER (WHERE el.provider IS NOT NULL),
				'[]'
			) AS rate_limits
		FROM provider_models pm
		LEFT JOIN effective_limits el
			ON el.provider = pm.provider AND (el.model_id = pm.model_id OR el.model_id = '')
		LEFT JOIN model_perf_stats mps
			ON mps.provider = pm.provider AND mps.model_id = pm.model_id
		`+whereClause+`
		GROUP BY pm.provider, pm.model_id, pm.display_name, pm.context_window, pm.max_output_tokens,
			pm.input_cost_per_mtok, pm.output_cost_per_mtok, pm.parameter_count_b, pm.architecture,
			pm.score, pm.avg_latency_ms, pm.error_rate, pm.probe_status,
			mps.total_requests, mps.total_errors, mps.p50_latency_ms, mps.p95_latency_ms, mps.p99_latency_ms,
			mps.min_latency_ms, mps.max_latency_ms
		ORDER BY pm.provider, pm.model_id`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list models")
		return
	}
	defer rows.Close()

	out := make([]modelJSON, 0)
	for rows.Next() {
		var m modelJSON
		var rateLimitsJSON []byte
		if err := rows.Scan(
			&m.Provider, &m.ModelID, &m.DisplayName, &m.ContextWindow, &m.MaxOutputTokens,
			&m.InputCostPerMTok, &m.OutputCostPerMTok,
			&m.ParameterCountB, &m.Architecture,
			&m.Score, &m.AvgLatencyMs, &m.ErrorRate, &m.ProbeStatus,
			&m.TotalRequests, &m.TotalErrors,
			&m.P50LatencyMs, &m.P95LatencyMs, &m.P99LatencyMs,
			&m.MinLatencyMs, &m.MaxLatencyMs,
			&rateLimitsJSON,
		); err != nil {
			continue
		}
		json.Unmarshal(rateLimitsJSON, &m.RateLimits)
		// Build score breakdown regardless of whether data is available
		bd := scoreBreakdown{
			Formula:            "score = (1-error_rate)*0.35 + (1-latency_ratio)*0.30 + 0.20 + 1.0*0.15",
			ErrorRate:          m.ErrorRate,
			AvgLatencyMs:       m.AvgLatencyMs,
			ReliabilityWeight:  0.35,
			LatencyWeight:      0.30,
			BaselineWeight:     0.20,
			AvailabilityWeight: 0.15,
			DataSufficient:     m.Score != nil,
		}
		if m.ErrorRate != nil {
			v := (1 - *m.ErrorRate) * 0.35
			bd.ReliabilityScore = &v
		}
		if m.Score != nil {
			bd.FinalScore = m.Score
			if m.ErrorRate != nil && m.AvgLatencyMs != nil {
				// latency_ratio = avg_latency_ms / 5000 (max baseline)
				latencyRatio := float64(*m.AvgLatencyMs) / 5000.0
				if latencyRatio > 1 {
					latencyRatio = 1
				}
				v := (1 - latencyRatio) * 0.30
				bd.LatencyScore = &v
			}
		}
		m.ScoreBreakdown = bd
		out = append(out, m)
	}
	if provider != "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"models": out, "provider": provider})
	} else {
		writeJSON(w, http.StatusOK, map[string]interface{}{"models": out})
	}
}

// TriggerDiscovery manually triggers model discovery for all providers with admin keys.
func (h *AdminHandler) TriggerDiscovery(w http.ResponseWriter, r *http.Request) {
	go h.discovery.Run(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "discovery_started"})
}

// ResetPool flushes all Redis pool keys for every user and re-seeds from DB.
// This forces the router to re-evaluate all combos from a clean slate.
func (h *AdminHandler) ResetPool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Collect all user IDs
	rows, err := h.pgPool.Query(ctx, `SELECT id FROM users WHERE status = 'active'`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list users")
		return
	}
	var userIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			userIDs = append(userIDs, id)
		}
	}
	rows.Close()

	// Flush all tier pool keys
	var keysDeleted int
	for _, uid := range userIDs {
		for _, tier := range []routing.Tier{routing.TierFlashLite, routing.TierFlash, routing.TierPro} {
			h.redis.Del(ctx, fmt.Sprintf("pool:%s:%s", uid, tier.String()))
			keysDeleted++
		}
	}

	// Re-seed pool from provider_keys + probe_ok models with tier assignment
	pkRows, err := h.pgPool.Query(ctx,
		`SELECT pk.id, pk.user_id, pk.provider
		 FROM provider_keys pk WHERE pk.status = 'active'`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to seed pool")
		return
	}
	seeded := 0
	for pkRows.Next() {
		var keyID, userID, provider string
		if pkRows.Scan(&keyID, &userID, &provider) != nil {
			continue
		}
		modelRows, err := h.pgPool.Query(ctx,
			`SELECT model_id, COALESCE(parameter_count_b, 0), context_window
			 FROM provider_models
			 WHERE provider = $1 AND probe_status = 'probe_ok'
			   AND parameter_count_b > 0`, provider)
		if err != nil {
			continue
		}
		for modelRows.Next() {
			var modelID string
			var paramB float64
			var ctxWindow int
			if modelRows.Scan(&modelID, &paramB, &ctxWindow) == nil {
				h.routingPool.AddComboToAll(ctx, userID, provider, modelID, keyID, paramB, ctxWindow)
				seeded++
			}
		}
		modelRows.Close()
	}
	pkRows.Close()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "pool_reset",
		"users":       len(userIDs),
		"combos_seeded": seeded,
	})
}

// PerfTest hits every probe_ok model directly using provider keys, logs results
// to request_metadata so they feed into scoring, and returns per-model results.
func (h *AdminHandler) PerfTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get admin user ID so we can log to request_metadata under their account
	adminUserID := auth.UserIDFromCtx(ctx)

	// Get ALL probe_ok models with context_window >= 100K
	rows, err := h.pgPool.Query(ctx,
		`SELECT pm.provider, pm.model_id, pk.id AS key_id, pk.ciphertext, pk.nonce
		 FROM provider_models pm
		 JOIN provider_keys pk ON pk.provider = pm.provider
		   AND pk.status = 'active'
		   AND COALESCE(pk.last_test_result,'ok') NOT LIKE 'error:%'
		 WHERE pm.probe_status = 'probe_ok'
		   AND pm.context_window >= 100000
		   AND pm.parameter_count_b > 0
		 ORDER BY pm.provider, pm.model_id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list models")
		return
	}

	type target struct {
		provider  string
		model     string
		keyID     string
		apiKey    string
	}
	seen := map[string]bool{} // deduplicate provider+model (use first key found)
	var targets []target
	for rows.Next() {
		var provider, model, keyID string
		var ciphertext, nonce []byte
		if rows.Scan(&provider, &model, &keyID, &ciphertext, &nonce) != nil {
			continue
		}
		dedupeKey := provider + ":" + model
		if seen[dedupeKey] {
			continue
		}
		dec, err := keys.Decrypt(ciphertext, nonce)
		if err != nil {
			continue
		}
		seen[dedupeKey] = true
		targets = append(targets, target{provider, model, keyID, dec})
	}
	rows.Close()

	type result struct {
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		OK        bool   `json:"ok"`
		LatencyMs int64  `json:"latency_ms"`
		Response  string `json:"response,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	prompts := []string{
		"What is 2+2? One word.",
		"Name the capital of Japan. One word.",
		"What color is the sun? One word.",
	}

	var mu sync.Mutex
	var results []result
	sem := make(chan struct{}, 6) // max 6 concurrent to avoid rate limits
	var wg sync.WaitGroup

	for i, t := range targets {
		t := t
		prompt := prompts[i%len(prompts)]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			prov, err := providers.Get(t.provider)
			if err != nil {
				mu.Lock()
				results = append(results, result{Provider: t.provider, Model: t.model, OK: false, Error: "unknown provider"})
				mu.Unlock()
				return
			}
			req := &providers.ChatRequest{
				Model:     t.model,
				Messages:  []providers.ChatMessage{{Role: "user", Content: prompt}},
				MaxTokens: 20,
			}
			start := time.Now()
			resp, err := prov.Complete(ctx, req, t.apiKey)
			latency := time.Since(start).Milliseconds()

			res := result{Provider: t.provider, Model: t.model, LatencyMs: latency}
			status := "success"
			errMsg := ""
			if err != nil {
				res.Error = err.Error()
				status = "error"
				errMsg = err.Error()
			} else {
				res.OK = true
				res.Response = resp.Content
			}

			// Log to request_metadata so this feeds into scoring
			h.pgPool.Exec(ctx, `
				INSERT INTO request_metadata
					(user_id, provider, model, latency_ms, status, error_message, provider_key_id, pool, input_tokens, output_tokens)
				VALUES ($1,$2,$3,$4,$5,$6,$7::uuid,'perf_test',1,20)`,
				adminUserID, t.provider, t.model, int(latency), status, errMsg, t.keyID)

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}
	wg.Wait()

	pass, fail := 0, 0
	for _, res := range results {
		if res.OK {
			pass++
		} else {
			fail++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"summary": map[string]int{"total": len(results), "pass": pass, "fail": fail},
	})
}

func (h *AdminHandler) GetPoolForUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	type comboJSON struct {
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		ProviderKeyID string `json:"provider_key_id"`
		Pool          string `json:"pool"`
	}
	toJSON := func(combos []routing.Combo) []comboJSON {
		out := make([]comboJSON, 0, len(combos))
		for _, c := range combos {
			out = append(out, comboJSON{c.Provider, c.Model, c.ProviderKeyID, c.Pool})
		}
		return out
	}
	result := map[string]interface{}{}
	for _, tier := range []routing.Tier{routing.TierFlashLite, routing.TierFlash, routing.TierPro} {
		combos, err := h.routingPool.LoadPool(r.Context(), userID, tier)
		if err != nil {
			continue
		}
		result[tier.String()] = toJSON(combos)
	}
	writeJSON(w, http.StatusOK, result)
}
