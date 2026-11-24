package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/models"
	"github.com/zer0-router/zer0/internal/orgs"
	"github.com/zer0-router/zer0/internal/providers"
)

type AdminHandler struct {
	pgPool    *pgxpool.Pool
	orgs      *orgs.Repository
	registry  *models.Registry
	discovery *models.DiscoveryJob
}

func NewAdminHandler(pg *pgxpool.Pool, registry *models.Registry, discovery *models.DiscoveryJob) *AdminHandler {
	return &AdminHandler{pgPool: pg, orgs: orgs.NewRepository(pg), registry: registry, discovery: discovery}
}

var adminUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CreateInvite: POST /admin/invites. Optional org_id (+ org_role admin|member) invites into
// an existing org; without org_id the invitee gets a new org of their own.
func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	adminID := auth.UserIDFromCtx(r.Context())
	var req struct {
		Email   string `json:"email"`
		OrgID   string `json:"org_id"`
		OrgRole string `json:"org_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_request", "email is required")
		return
	}
	if req.OrgID == "" && req.OrgRole != "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "org_role_without_org", "org_role requires org_id")
		return
	}
	if req.OrgRole != "" && req.OrgRole != orgs.RoleAdmin && req.OrgRole != orgs.RoleMember {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_org_role", "org_role must be admin or member")
		return
	}
	if req.OrgID != "" {
		if !adminUUIDRe.MatchString(req.OrgID) {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_org_id", "org_id must be a UUID")
			return
		}
		ok, err := h.orgs.Exists(r.Context(), req.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to look up org")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "org_not_found", "Org not found")
			return
		}
		if req.OrgRole == "" {
			req.OrgRole = orgs.RoleMember
		}
	}

	var token string
	var expiresAt time.Time
	err := h.pgPool.QueryRow(r.Context(),
		`INSERT INTO invites (email, token, created_by, invited_by, expires_at, org_id, org_role)
		 VALUES ($1, gen_random_uuid()::text, $2, $2, NOW() + INTERVAL '7 days', NULLIF($3, '')::uuid, NULLIF($4, ''))
		 RETURNING token, expires_at`,
		req.Email, adminID, req.OrgID, req.OrgRole,
	).Scan(&token, &expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create invite")
		return
	}

	resp := map[string]interface{}{
		"token":      token,
		"email":      req.Email,
		"expires_at": expiresAt.Format(time.RFC3339),
	}
	if req.OrgID != "" {
		resp["org_id"] = req.OrgID
		resp["org_role"] = req.OrgRole
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ListOrgs: GET /admin/orgs. Orgs with member, live-token and active-key counts.
func (h *AdminHandler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	list, err := h.orgs.ListSummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list orgs")
		return
	}
	type orgOut struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		CreatedAt   string `json:"created_at"`
		MemberCount int    `json:"member_count"`
		TokenCount  int    `json:"token_count"`
		KeyCount    int    `json:"key_count"`
	}
	out := make([]orgOut, 0, len(list))
	for _, o := range list {
		out = append(out, orgOut{o.ID, o.Name, o.CreatedAt.Format(time.RFC3339), o.MemberCount, o.TokenCount, o.KeyCount})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"orgs": out})
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pgPool.Query(r.Context(),
		`SELECT u.id::text, u.email, u.role, u.status, u.created_at, o.id::text, o.name, m.role
		   FROM users u
		   LEFT JOIN org_members m ON m.user_id = u.id
		   LEFT JOIN orgs o ON o.id = m.org_id
		  ORDER BY u.created_at DESC`,
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
		Org       *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"org"`
	}
	var users []userJSON
	for rows.Next() {
		var u userJSON
		var createdAt time.Time
		var orgID, orgName, orgRole *string
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &createdAt, &orgID, &orgName, &orgRole); err != nil {
			continue
		}
		if orgID != nil && orgName != nil && orgRole != nil {
			u.Org = &struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Role string `json:"role"`
			}{*orgID, *orgName, *orgRole}
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
		ReliabilityWeight  float64 `json:"reliability_weight"`
		LatencyWeight      float64 `json:"latency_weight"`
		BaselineWeight     float64 `json:"baseline_weight"`
		AvailabilityWeight float64 `json:"availability_weight"`
		// Component values (nil if not enough data)
		ReliabilityScore *float64 `json:"reliability_score"`
		LatencyScore     *float64 `json:"latency_score"`
		FinalScore       *float64 `json:"final_score"`
		DataSufficient   bool     `json:"data_sufficient"`
	}
	type modelJSON struct {
		Provider          string   `json:"provider"`
		ModelID           string   `json:"model_id"`
		DisplayName       string   `json:"display_name"`
		ContextWindow     int      `json:"context_window"`
		MaxOutputTokens   int      `json:"max_output_tokens"`
		InputCostPerMTok  float64  `json:"input_cost_per_mtok"`
		OutputCostPerMTok float64  `json:"output_cost_per_mtok"`
		ParameterCountB   *float64 `json:"parameter_count_b"`
		Architecture      *string  `json:"architecture"`
		Score             *float64 `json:"score"`
		AvgLatencyMs      *int     `json:"avg_latency_ms"`
		ErrorRate         *float64 `json:"error_rate"`
		ProbeStatus       string   `json:"probe_status"`
		// Historical stats (always null since model_perf_stats was dropped in 000022)
		TotalRequests  *int64          `json:"total_requests"`
		TotalErrors    *int64          `json:"total_errors"`
		P50LatencyMs   *int            `json:"p50_latency_ms"`
		P95LatencyMs   *int            `json:"p95_latency_ms"`
		P99LatencyMs   *int            `json:"p99_latency_ms"`
		MinLatencyMs   *int            `json:"min_latency_ms"`
		MaxLatencyMs   *int            `json:"max_latency_ms"`
		ScoreBreakdown scoreBreakdown  `json:"score_breakdown"`
		RateLimits     []rateLimitJSON `json:"rate_limits"`
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
			-- Historical per-model stats came from model_perf_stats, dropped in
			-- migration 000022; the fields are kept in the response as null.
			NULL::bigint, NULL::bigint,
			NULL::int, NULL::int, NULL::int,
			NULL::int, NULL::int,
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
		`+whereClause+`
		GROUP BY pm.provider, pm.model_id, pm.display_name, pm.context_window, pm.max_output_tokens,
			pm.input_cost_per_mtok, pm.output_cost_per_mtok, pm.parameter_count_b, pm.architecture,
			pm.score, pm.avg_latency_ms, pm.error_rate, pm.probe_status
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
		provider string
		model    string
		keyID    string
		apiKey   string
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
