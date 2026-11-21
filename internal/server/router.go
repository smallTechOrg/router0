// Package server holds the single route table of the router0 gateway. Both
// cmd/router and the integration tests build the HTTP handler here, so tests
// exercise exactly the production wiring.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/handlers"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/metadata"
	"github.com/zer0-router/zer0/internal/models"
	"github.com/zer0-router/zer0/internal/orgs"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
	"github.com/zer0-router/zer0/internal/tokens"
)

// Deps are the injectable inputs of the server. Provider base URLs are read by
// the adapters from *_BASE_URL env vars at call time (tests use t.Setenv), and
// the AES key from AES_KEY, so neither appears here.
type Deps struct {
	Pool      *pgxpool.Pool
	JWTSecret string

	// TokenCacheTTL is the service-token cache TTL (0 = ROUTER0 default, 30 s).
	TokenCacheTTL time.Duration
	// KeyStateOptions tunes the key-state store (cache TTL, cooldown lengths, clock).
	KeyStateOptions keystate.Options
	// ExecutorConfig lets tests tune attempts/timeouts. States, Auth and Catalog are always set by Build.
	ExecutorConfig routing.Config
	// Inference tunes the inference handler (FirstByteTimeout, Decrypt). Wiring fields are set by Build.
	Inference handlers.InferenceConfig
}

// App is a built gateway: the HTTP handler plus the components main needs for
// background jobs and shutdown.
type App struct {
	Handler   http.Handler
	Recorder  *metadata.Recorder
	KeyState  *keystate.PGStore
	Catalog   *catalog.Repository
	Discovery *catalog.Discovery
	Scheduler *catalog.Scheduler
}

// Close flushes the request-log recorder.
func (a *App) Close(ctx context.Context) error { return a.Recorder.Close(ctx) }

// New builds the gateway HTTP handler. Callers that need to flush the request
// log on shutdown use Build instead.
func New(d Deps) http.Handler { return Build(d).Handler }

// Build wires repositories, handlers and the route table.
func Build(d Deps) *App {
	pool := d.Pool

	orgRepo := orgs.NewRepository(pool)
	tokenRepo := tokens.NewRepository(pool)
	verifier := tokens.NewVerifier(tokenRepo, d.TokenCacheTTL, 10000)

	keyRepo := keys.NewRepository(pool)
	catalogRepo := catalog.NewRepository(pool)
	discovery := catalog.NewDiscovery(catalogRepo, catalog.NewKeySource(keyRepo))
	resolver := catalog.NewResolver(catalogRepo, discovery)
	keyStates := keystate.New(pool, d.KeyStateOptions)

	modelRegistry := models.NewRegistry(pool)
	legacyDiscovery := models.NewDiscoveryJob(pool)

	authHandler := handlers.NewAuthHandler(pool, d.JWTSecret)
	tokensHandler := handlers.NewTokensHandler(tokenRepo)
	adminHandler := handlers.NewAdminHandler(pool, modelRegistry, legacyDiscovery)
	keysHandler := handlers.NewKeysHandler(pool, handlers.KeysDeps{
		ValidProviders: providers.ValidProviders,
		Discoverer:     discovery,
	})
	priorityHandler := handlers.NewPriorityHandler(keysHandler.Priority(), providers.ValidProviders)
	v1Catalog := handlers.NewCatalogHandler(catalogRepo, discovery, func(r *http.Request) string {
		p, _ := auth.PrincipalFromCtx(r.Context())
		return p.OrgID
	})
	apiCatalog := handlers.NewCatalogHandler(catalogRepo, discovery, func(r *http.Request) string {
		return auth.OrgIDFromCtx(r.Context())
	})
	aliasesHandler := handlers.NewAliasesHandler(catalogRepo, func(r *http.Request) string {
		return auth.OrgIDFromCtx(r.Context())
	}, func(r *http.Request) string {
		return auth.UserIDFromCtx(r.Context())
	})
	requestLog := handlers.NewRequestLogHandler(pool)

	recorder := metadata.NewRecorder(pool)
	execCfg := d.ExecutorConfig
	execCfg.States = keyStates
	execCfg.Auth = keyRepo
	execCfg.Catalog = catalogRepo
	inf := d.Inference
	inf.Resolver = resolver
	inf.Priority = keysHandler.Priority()
	inf.Keys = keyRepo
	inf.Executor = routing.NewExecutor(execCfg)
	inf.Recorder = recorder
	inferenceHandler := handlers.NewInferenceHandler(inf)

	jwt := auth.JWTMiddleware(d.JWTSecret, orgRepo)
	serviceToken := auth.ServiceTokenMiddleware(verifier)
	orgWrite := auth.RequireOrgRole(orgs.RoleOwner, orgs.RoleAdmin)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthz(pool))
	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	// Auth (humans)
	r.Post("/auth/register", authHandler.Register)
	r.Post("/auth/login", authHandler.Login)
	r.Post("/auth/logout", authHandler.Logout)
	r.Post("/auth/refresh", authHandler.Refresh)
	r.With(jwt).Get("/auth/me", authHandler.Me)

	// Inference (service tokens)
	r.Group(func(r chi.Router) {
		r.Use(serviceToken)
		r.Get("/v1/models", v1Catalog.ListModels)
		r.Post("/v1/chat/completions", inferenceHandler.ChatCompletions)
	})

	// Dashboard API (JWT, org-scoped). Writes need org role owner/admin.
	r.Group(func(r chi.Router) {
		r.Use(jwt)

		r.Get("/api/tokens", tokensHandler.List)
		r.With(orgWrite).Post("/api/tokens", tokensHandler.Create)
		r.With(orgWrite).Delete("/api/tokens/{id}", tokensHandler.Revoke)

		r.Get("/api/providers", priorityHandler.ListProviders)
		r.Get("/api/keys", keysHandler.ListKeys)
		r.With(orgWrite).Post("/api/keys", keysHandler.AddKey)
		r.With(orgWrite).Delete("/api/keys/{id}", keysHandler.DeleteKey)
		r.With(orgWrite).Post("/api/keys/{id}/test", keysHandler.TestKey)

		r.Get("/api/routing/priority", priorityHandler.GetPriority)
		r.With(orgWrite).Put("/api/routing/priority", priorityHandler.PutPriority)

		r.Get("/api/catalog", apiCatalog.GetCatalog)
		r.With(orgWrite).Post("/api/catalog/refresh", apiCatalog.RefreshCatalog)

		r.Get("/api/aliases", aliasesHandler.List)
		r.With(orgWrite).Post("/api/aliases", aliasesHandler.Create)
		r.With(orgWrite).Delete("/api/aliases/{alias}", aliasesHandler.Delete)

		r.Get("/api/requests", requestLog.ListRequests)
		r.Get("/api/requests/{id}", requestLog.GetRequest)
		r.Get("/api/costs", requestLog.GetCosts)
	})

	// Platform admin (JWT + role=admin)
	r.Group(func(r chi.Router) {
		r.Use(jwt)
		r.Use(auth.AdminOnly)
		r.Post("/admin/invites", adminHandler.CreateInvite)
		r.Get("/admin/users", adminHandler.ListUsers)
		r.Get("/admin/orgs", adminHandler.ListOrgs)
		r.Post("/admin/provider-keys", adminHandler.SetProviderKey)
		r.Get("/admin/provider-keys", adminHandler.ListProviderKeys)
		r.Get("/admin/models", adminHandler.ListModels)
		r.Post("/admin/discovery", adminHandler.TriggerDiscovery)
		r.Post("/admin/perf-test", adminHandler.PerfTest)
	})

	return &App{
		Handler:   r,
		Recorder:  recorder,
		KeyState:  keyStates,
		Catalog:   catalogRepo,
		Discovery: discovery,
		Scheduler: catalog.NewScheduler(catalogRepo, discovery, catalog.IntervalFromEnv()),
	}
}

// healthz reports database connectivity only.
func healthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		}{Status: "ok", DB: "ok"}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			resp.Status, resp.DB = "degraded", "error"
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
