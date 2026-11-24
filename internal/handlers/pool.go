package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
)

// RequestLogHandler serves the org-scoped request log and cost endpoints.
type RequestLogHandler struct {
	db *pgxpool.Pool
}

func NewRequestLogHandler(db *pgxpool.Pool) *RequestLogHandler {
	return &RequestLogHandler{db: db}
}

type trailEntry struct {
	Order      int     `json:"order"`
	Provider   string  `json:"provider"`
	KeyLabel   *string `json:"key_label"`
	Outcome    string  `json:"outcome"`
	Class      *string `json:"class,omitempty"`
	HTTPStatus *int    `json:"http_status,omitempty"`
	SkipReason *string `json:"skip_reason,omitempty"`
	RetryAfter *int    `json:"retry_after_s,omitempty"`
	ErrorCode  *string `json:"error_code,omitempty"`
	LatencyMs  int     `json:"latency_ms"`
}

type requestItem struct {
	RequestID        string       `json:"request_id"`
	CreatedAt        string       `json:"created_at"`
	TokenName        *string      `json:"token_name"`
	CanonicalModel   string       `json:"canonical_model"`
	Provider         string       `json:"provider"`
	ProviderModelID  string       `json:"provider_model_id"`
	KeyLabel         *string      `json:"key_label"`
	Status           string       `json:"status"`
	HTTPStatus       *int         `json:"http_status"`
	ErrorCode        *string      `json:"error_code"`
	Stream           bool         `json:"stream"`
	LatencyMs        int          `json:"latency_ms"`
	InputTokens      int          `json:"input_tokens"`
	OutputTokens     int          `json:"output_tokens"`
	EstimatedCostUSD float64      `json:"estimated_cost_usd"`
	AttemptCount     int          `json:"attempt_count"`
	Trail            []trailEntry `json:"trail"`
}

type requestFilter struct {
	Model    string
	Provider string
	Status   string
	TokenID  string
	Since    *time.Time // created_at >= Since
	Until    *time.Time // created_at < Until
	Before   *time.Time // cursor: created_at < Before
	Limit    int
}

type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const requestSelect = `
SELECT rm.zer0_request_id, rm.created_at, st.name,
       COALESCE(rm.canonical_model, rm.model), rm.provider, rm.model, rm.provider_key_label,
       rm.status, rm.http_status::int, rm.error_code, rm.stream,
       rm.latency_ms, rm.input_tokens, rm.output_tokens, rm.estimated_cost_usd::float8,
       rm.attempt_count::int
FROM request_metadata rm
LEFT JOIN service_tokens st ON st.id = rm.service_token_id AND st.org_id = rm.org_id
`

func scanRequests(rows pgx.Rows) ([]requestItem, error) {
	defer rows.Close()
	items := []requestItem{}
	for rows.Next() {
		var it requestItem
		var created time.Time
		var rid *string
		if err := rows.Scan(&rid, &created, &it.TokenName, &it.CanonicalModel, &it.Provider,
			&it.ProviderModelID, &it.KeyLabel, &it.Status, &it.HTTPStatus, &it.ErrorCode, &it.Stream,
			&it.LatencyMs, &it.InputTokens, &it.OutputTokens, &it.EstimatedCostUSD, &it.AttemptCount); err != nil {
			return nil, err
		}
		if rid != nil {
			it.RequestID = *rid
		}
		it.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		it.Trail = []trailEntry{}
		items = append(items, it)
	}
	return items, rows.Err()
}

// attachTrails loads attempts for the given requests (org-scoped) in one query.
func attachTrails(ctx context.Context, db pgQuerier, orgID string, items []requestItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	idx := make(map[string]*requestItem, len(items))
	for i := range items {
		if items[i].RequestID == "" {
			continue
		}
		ids = append(ids, items[i].RequestID)
		idx[items[i].RequestID] = &items[i]
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := db.Query(ctx, `
		SELECT zer0_request_id, attempt_order::int, provider, provider_key_label, outcome,
		       error_class, http_status::int, skip_reason, retry_after_s, provider_error_code, latency_ms
		FROM request_attempts
		WHERE org_id = $1 AND zer0_request_id = ANY($2)
		ORDER BY zer0_request_id, attempt_order, created_at, id`, orgID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rid string
		var e trailEntry
		if err := rows.Scan(&rid, &e.Order, &e.Provider, &e.KeyLabel, &e.Outcome, &e.Class,
			&e.HTTPStatus, &e.SkipReason, &e.RetryAfter, &e.ErrorCode, &e.LatencyMs); err != nil {
			return err
		}
		if it, ok := idx[rid]; ok {
			it.Trail = append(it.Trail, e)
		}
	}
	return rows.Err()
}

// listRequests runs the org-scoped, filtered, keyset-paginated query.
// next is non-empty when more rows exist.
func listRequests(ctx context.Context, db pgQuerier, orgID string, f requestFilter) (items []requestItem, next string, err error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args := []any{orgID}
	where := "WHERE rm.org_id = $1"
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + replaceN(cond, len(args))
	}
	if f.Model != "" {
		add("COALESCE(rm.canonical_model, rm.model) = $N", f.Model)
	}
	if f.Provider != "" {
		add("rm.provider = $N", f.Provider)
	}
	if f.Status != "" {
		add("rm.status = $N", f.Status)
	}
	if f.TokenID != "" {
		add("rm.service_token_id = $N::uuid", f.TokenID)
	}
	if f.Since != nil {
		add("rm.created_at >= $N", *f.Since)
	}
	if f.Until != nil {
		add("rm.created_at < $N", *f.Until)
	}
	if f.Before != nil {
		add("rm.created_at < $N", *f.Before)
	}
	args = append(args, f.Limit+1)
	q := requestSelect + where + " ORDER BY rm.created_at DESC, rm.id DESC LIMIT $" + strconv.Itoa(len(args))
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	items, err = scanRequests(rows)
	if err != nil {
		return nil, "", err
	}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		next = items[len(items)-1].CreatedAt
	}
	if err := attachTrails(ctx, db, orgID, items); err != nil {
		return nil, "", err
	}
	return items, next, nil
}

func replaceN(s string, n int) string {
	out := make([]byte, 0, len(s)+3)
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == 'N' {
			out = append(out, '$')
			out = strconv.AppendInt(out, int64(n), 10)
			i++
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func getRequest(ctx context.Context, db pgQuerier, orgID, requestID string) (*requestItem, error) {
	rows, err := db.Query(ctx, requestSelect+"WHERE rm.org_id = $1 AND rm.zer0_request_id = $2 LIMIT 1", orgID, requestID)
	if err != nil {
		return nil, err
	}
	items, err := scanRequests(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if err := attachTrails(ctx, db, orgID, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func parseTime(s string) (*time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (h *RequestLogHandler) ListRequests(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	if orgID == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "Missing organization context")
		return
	}
	q := r.URL.Query()
	f := requestFilter{
		Model:    q.Get("model"),
		Provider: q.Get("provider"),
		Status:   q.Get("status"),
		TokenID:  q.Get("token_id"),
		Limit:    50,
	}
	if f.TokenID == "" {
		f.TokenID = q.Get("token")
	}
	bad := func(msg string) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_parameter", msg)
	}
	if f.Status != "" && f.Status != "success" && f.Status != "error" {
		bad("status must be success or error")
		return
	}
	if f.TokenID != "" && !isUUID(f.TokenID) {
		bad("token_id must be a UUID")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			bad("limit must be a positive integer")
			return
		}
		if n > 200 {
			n = 200
		}
		f.Limit = n
	}
	for name, dst := range map[string]**time.Time{"before": &f.Before, "since": &f.Since, "until": &f.Until} {
		if v := q.Get(name); v != "" {
			t, err := parseTime(v)
			if err != nil {
				bad(name + " must be an RFC3339 timestamp")
				return
			}
			*dst = t
		}
	}
	items, next, err := listRequests(r.Context(), h.db, orgID, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "db_error", "Failed to fetch requests")
		return
	}
	var nb any
	if next != "" {
		nb = next
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": items, "next_before": nb})
}

func (h *RequestLogHandler) GetRequest(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	if orgID == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "Missing organization context")
		return
	}
	it, err := getRequest(r.Context(), h.db, orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "db_error", "Failed to fetch request")
		return
	}
	if it == nil {
		writeError(w, http.StatusNotFound, "invalid_request_error", "not_found", "Request not found")
		return
	}
	writeJSON(w, http.StatusOK, it)
}

type costRow struct {
	Provider         string  `json:"provider"`
	CanonicalModel   string  `json:"canonical_model"`
	KeyLabel         *string `json:"key_label"`
	RequestCount     int64   `json:"request_count"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

func costs(ctx context.Context, db pgQuerier, orgID string) ([]costRow, float64, error) {
	rows, err := db.Query(ctx, `
		SELECT provider, COALESCE(canonical_model, model), provider_key_label,
		       COUNT(*), COALESCE(SUM(input_tokens),0)::bigint, COALESCE(SUM(output_tokens),0)::bigint,
		       COALESCE(SUM(estimated_cost_usd),0)::float8
		FROM request_metadata
		WHERE org_id = $1 AND created_at >= NOW() - INTERVAL '30 days'
		GROUP BY provider, COALESCE(canonical_model, model), provider_key_label
		ORDER BY 7 DESC, 1, 2`, orgID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []costRow{}
	var total float64
	for rows.Next() {
		var c costRow
		if err := rows.Scan(&c.Provider, &c.CanonicalModel, &c.KeyLabel, &c.RequestCount,
			&c.InputTokens, &c.OutputTokens, &c.EstimatedCostUSD); err != nil {
			return nil, 0, err
		}
		total += c.EstimatedCostUSD
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// tokenCostRow is one service token's last-30-days usage (GET /api/costs by_token).
// Requests without a token (none in practice) and revoked tokens are included.
type tokenCostRow struct {
	TokenID          *string `json:"token_id"`
	TokenName        *string `json:"token_name"`
	Revoked          bool    `json:"revoked"`
	RequestCount     int64   `json:"request_count"`
	ErrorCount       int64   `json:"error_count"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

func costsByToken(ctx context.Context, db pgQuerier, orgID string) ([]tokenCostRow, error) {
	rows, err := db.Query(ctx, `
		SELECT rm.service_token_id::text, t.name, t.revoked_at IS NOT NULL,
		       COUNT(*), COUNT(*) FILTER (WHERE rm.status = 'error'),
		       COALESCE(SUM(rm.estimated_cost_usd),0)::float8
		FROM request_metadata rm
		LEFT JOIN service_tokens t ON t.id = rm.service_token_id
		WHERE rm.org_id = $1 AND rm.created_at >= NOW() - INTERVAL '30 days'
		GROUP BY rm.service_token_id, t.name, t.revoked_at
		ORDER BY 6 DESC, 4 DESC, 2`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tokenCostRow{}
	for rows.Next() {
		var c tokenCostRow
		var revoked *bool
		if err := rows.Scan(&c.TokenID, &c.TokenName, &revoked, &c.RequestCount, &c.ErrorCount, &c.EstimatedCostUSD); err != nil {
			return nil, err
		}
		c.Revoked = revoked != nil && *revoked && c.TokenID != nil
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *RequestLogHandler) GetCosts(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	if orgID == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "Missing organization context")
		return
	}
	rows, total, err := costs(r.Context(), h.db, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "db_error", "Failed to fetch costs")
		return
	}
	byToken, err := costsByToken(r.Context(), h.db, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "db_error", "Failed to fetch costs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window_days":              30,
		"costs":                    rows,
		"by_token":                 byToken,
		"total_estimated_cost_usd": total,
	})
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}
