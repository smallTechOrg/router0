package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/zer0-router/zer0/internal/catalog"
)

// CatalogHandler serves GET /v1/models, GET /api/catalog and POST /api/catalog/refresh.
type CatalogHandler struct {
	repo  *catalog.Repository
	disc  catalog.Discoverer
	orgID func(*http.Request) string
	// allows, when set, limits ListModels to the caller's allowed models
	// (service-token allowlists on /v1/models).
	allows func(*http.Request) func(canonical, requested string) bool
}

// WithModelFilter limits ListModels to the models fn's predicate allows.
func (h *CatalogHandler) WithModelFilter(fn func(*http.Request) func(canonical, requested string) bool) *CatalogHandler {
	h.allows = fn
	return h
}

// NewCatalogHandler builds the handler. orgID extracts the caller's org id from
// the request (service-token Principal for /v1/models, JWT org for /api/*);
// returning "" yields 401. Role gating (owner/admin on refresh) is applied by
// the router via auth.RequireOrgRole.
func NewCatalogHandler(repo *catalog.Repository, disc catalog.Discoverer, orgID func(*http.Request) string) *CatalogHandler {
	return &CatalogHandler{repo: repo, disc: disc, orgID: orgID}
}

type provModel struct {
	Provider        string `json:"provider"`
	ProviderModelID string `json:"provider_model_id"`
}

// ListModels handles GET /v1/models: available chat canonical models plus the
// org's aliases (router0.alias_of), alphabetical.
func (h *CatalogHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	rows, err := h.repo.List(r.Context(), org, "", catalog.KindChat, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to list models")
		return
	}
	aliases, err := h.repo.ListAliases(r.Context(), org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to list models")
		return
	}
	type item struct {
		ID      string         `json:"id"`
		Object  string         `json:"object"`
		Created int64          `json:"created"`
		OwnedBy string         `json:"owned_by"`
		Router0 map[string]any `json:"router0"`
	}
	data := []item{}
	idx := map[string]int{}
	provs := map[string][]provModel{}
	for _, row := range rows {
		key := row.Canonical
		if _, ok := idx[key]; !ok {
			idx[key] = len(data)
			data = append(data, item{ID: row.Canonical, Object: "model", Created: row.LastSeenAt.Unix(), OwnedBy: "router0",
				Router0: map[string]any{"kind": catalog.KindChat}})
		}
		provs[key] = append(provs[key], provModel{row.Provider, row.ProviderModelID})
	}
	for i := range data {
		data[i].Router0["providers"] = provs[data[i].ID]
	}
	// Aliases whose target is currently servable; the real model wins on a name clash.
	for _, a := range aliases {
		ti, ok := idx[a.CanonicalModel]
		if !ok {
			continue
		}
		if _, clash := idx[a.Alias]; clash {
			continue
		}
		data = append(data, item{ID: a.Alias, Object: "model", Created: a.CreatedAt.Unix(), OwnedBy: "router0",
			Router0: map[string]any{"kind": catalog.KindChat, "alias_of": data[ti].ID}})
	}
	if h.allows != nil {
		allow := h.allows(r)
		kept := data[:0]
		for _, it := range data {
			target, _ := it.Router0["alias_of"].(string)
			if target == "" {
				target = it.ID
			}
			// A model is listed when a request for this id would be allowed.
			if allow(target, it.ID) {
				kept = append(kept, it)
			}
		}
		data = kept
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// GetCatalog handles GET /api/catalog[?q=][&kind=]. All kinds unless kind is given.
func (h *CatalogHandler) GetCatalog(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !catalog.ValidKind(kind) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "invalid_kind", "kind must be one of chat, image, audio, embedding, other")
		return
	}
	rows, err := h.repo.List(r.Context(), org, r.URL.Query().Get("q"), kind, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to load catalog")
		return
	}
	type provRow struct {
		Provider        string    `json:"provider"`
		ProviderModelID string    `json:"provider_model_id"`
		Kind            string    `json:"kind"`
		Source          string    `json:"source"`
		Available       bool      `json:"available"`
		LastSeenAt      time.Time `json:"last_seen_at"`
	}
	type model struct {
		Canonical string    `json:"canonical"`
		Kind      string    `json:"kind"`
		Providers []provRow `json:"providers"`
	}
	models := []model{}
	idx := map[[2]string]int{} // (canonical, kind): a canonical name can carry different kinds on different providers
	for _, row := range rows {
		k := [2]string{row.Canonical, row.Kind}
		i, ok := idx[k]
		if !ok {
			i = len(models)
			idx[k] = i
			models = append(models, model{Canonical: row.Canonical, Kind: row.Kind, Providers: []provRow{}})
		}
		models[i].Providers = append(models[i].Providers, provRow{row.Provider, row.ProviderModelID, row.Kind, row.Source, row.Available, row.LastSeenAt})
	}
	refs, err := h.repo.Refreshes(r.Context(), org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to load catalog")
		return
	}
	aliases, err := h.repo.ListAliases(r.Context(), org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to load catalog")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "refreshes": refreshJSON(refs, nil), "aliases": aliasJSON(aliases)})
}

// RefreshCatalog handles POST /api/catalog/refresh (owner/admin; gated by the router).
func (h *CatalogHandler) RefreshCatalog(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	var body struct {
		Provider string `json:"provider"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "invalid JSON body")
			return
		}
	}
	ctx := r.Context()
	provs, err := h.repo.OrgProviders(ctx, org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to refresh catalog")
		return
	}
	if body.Provider != "" {
		found := false
		for _, p := range provs {
			found = found || p == body.Provider
		}
		if !found {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "provider_not_configured", "no active key for provider "+body.Provider)
			return
		}
		provs = []string{body.Provider}
	}

	inProgress := map[string]bool{}
	failed := map[string]bool{}
	for _, p := range provs {
		rctx, cancel := context.WithTimeout(ctx, catalog.DefaultRefreshTimeout)
		_, err := h.disc.RefreshProvider(rctx, org, p)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, catalog.ErrInProgress):
			inProgress[p] = true
		default:
			failed[p] = true // the repository already recorded last_status=error where a listing was attempted
		}
	}
	refs, err := h.repo.Refreshes(ctx, org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to refresh catalog")
		return
	}
	want := map[string]bool{}
	for _, p := range provs {
		want[p] = true
	}
	var sel []catalog.Refresh
	have := map[string]bool{}
	for _, f := range refs {
		if want[f.Provider] {
			sel = append(sel, f)
			have[f.Provider] = true
		}
	}
	for _, p := range provs { // no stored row yet (e.g. no usable key, or first refresh still running)
		if have[p] {
			continue
		}
		if failed[p] {
			sel = append(sel, catalog.Refresh{Provider: p, LastRefreshedAt: time.Now().UTC(), LastStatus: "error", LastErrorClass: "unknown"})
		} else if inProgress[p] {
			sel = append(sel, catalog.Refresh{Provider: p, LastRefreshedAt: time.Now().UTC(), LastStatus: "in_progress"})
		}
	}
	sort.Slice(sel, func(i, j int) bool { return sel[i].Provider < sel[j].Provider })
	writeJSON(w, http.StatusOK, map[string]any{"refreshes": refreshJSON(sel, inProgress)})
}

func refreshJSON(refs []catalog.Refresh, inProgress map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, f := range refs {
		status := f.LastStatus
		if inProgress[f.Provider] {
			status = "in_progress"
		}
		m := map[string]any{
			"provider": f.Provider, "last_refreshed_at": f.LastRefreshedAt,
			"last_status": status, "model_count": f.ModelCount,
		}
		if f.LastErrorClass != "" {
			m["last_error_class"] = f.LastErrorClass
		}
		out = append(out, m)
	}
	return out
}
