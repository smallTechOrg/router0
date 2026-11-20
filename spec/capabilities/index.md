# Capability Index: router0

| # | Capability | File | Phase |
|---|-----------|------|-------|
| 1 | Orgs, Auth & Onboarding | [auth-onboarding.md](./auth-onboarding.md) | 1 |
| 2 | Service Tokens | [service-tokens.md](./service-tokens.md) | 1 |
| 3 | Provider Key Management (keys, key order, provider priority) | [provider-key-management.md](./provider-key-management.md) | 1 (budgets/reset/polish: 3) |
| 4 | Model Catalog (discovery, canonical names; aliases + on-demand: 2) | [model-catalog.md](./model-catalog.md) | 1 / 2 |
| 5 | Routing Engine (plan, failover, streaming) | [routing-engine.md](./routing-engine.md) | 1 |
| 6 | Error Classification & Key State | [error-classification.md](./error-classification.md) | 1 (Anthropic/Gemini rules: 2) |
| 7 | Provider Adapters | [provider-adapters.md](./provider-adapters.md) | 1 (Groq, OpenRouter) / 2 (Anthropic, Gemini, Mesh API) |
| 8 | Dashboard | [dashboard.md](./dashboard.md) | 1 / 2 (catalog page) / 3 (polish) |
| 9 | Observability (request log, metrics, logs) | [observability.md](./observability.md) | 1 |
| 10 | Embeddings | [embeddings.md](./embeddings.md) | 3 |
| 11 | Credit Tracking (balance polling, monthly budgets) | [credit-tracking.md](./credit-tracking.md) | 3 |

## Dependency Map

```
Orgs, Auth & Onboarding
  ├── Service Tokens ───────────────┐
  └── Provider Key Management       │
         ├── Model Catalog          │
         │       │                  ▼
         └───────┴──► Routing Engine ◄── Error Classification & Key State ◄── Provider Adapters
                            │
                            ├──► Observability (request log + trail)
                            ├──► Embeddings (P3, reuses executor)
                            └──► Credit Tracking (P3, feeds key state)
Dashboard: UI over Auth, Tokens, Keys/Priority, Catalog, Request Log
```

## Phase 1 Primary Journey (user-testable, mock providers or real keys)

Admin invites user → user registers (org created) → adds two Groq keys and one OpenRouter key → catalog auto-discovers models → sets priority `groq → openrouter` and Groq key order → creates service token `demo-svc` → calls `POST /v1/chat/completions` with `model: "llama-3.3-70b"` (streaming and not) → served by Groq key 1; when it is throttled, served by Groq key 2 or OpenRouter → when every key is throttled, gets `429 rate_limited` + `Retry-After`; out of credits → `402`; unknown model → `404` → request log shows provider, key label, latency, tokens, status, and failover trail → revokes the token → next call `401`.

## Phase 2 Expansion

Anthropic native adapter with full tool-calling translation, Gemini tool-calling conformance, Mesh API adapter, catalog page, org aliases, and on-demand discovery for unknown models.

## Phase 3 Expansion

`/v1/embeddings`, balance polling (OpenRouter), per-key monthly USD budgets, priority/key UI polish (drag-and-drop, reset key state), and deletion of the legacy pool/scorer code.
