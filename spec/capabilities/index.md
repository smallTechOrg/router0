# Capability Index

| # | Capability | File | Phase |
|---|-----------|------|-------|
| 1 | Auth & Onboarding | [auth-onboarding.md](./auth-onboarding.md) | Phase 1 |
| 2 | Provider Key Management | [provider-key-management.md](./provider-key-management.md) | Phase 1 |
| 3 | Provider Adapters | [provider-adapters.md](./provider-adapters.md) | Phase 1 |
| 4 | Routing Engine | [routing-engine.md](./routing-engine.md) | Phase 1 |
| 5 | Dashboard | [dashboard.md](./dashboard.md) | Phase 1 |
| 6 | Observability | [observability.md](./observability.md) | Phase 1 |
| 7 | Advanced Health Probes | (Phase 2 — extends Provider Key Management) | Phase 2 |
| 8 | Pool Auto-Management | (Phase 2 — extends Routing Engine) | Phase 2 |
| 9 | Cost Analytics | (Phase 2 — extends Dashboard) | Phase 2 |
| 10 | Admin API | (Phase 2 — extends Observability) | Phase 2 |

## Dependency Map

```
Auth & Onboarding
    └── Provider Key Management  (keys belong to users)
            └── Provider Adapters  (adapters use decrypted keys)
                    └── Routing Engine  (scores and selects adapters)
                            └── Dashboard  (visualises pool + requests)
Observability  (cross-cutting; wires into all layers at Phase 1)
```

## Phase 1 Primary Journey

Admin invites user → user registers and adds provider keys → user points OpenAI client at the router → router scores the active pool, picks the best model-provider combo, streams the response → user sees tokens; response headers name the provider and model → dashboard shows the request in the feed and pool health updates.

## Phase 2 Expansion

Advanced hybrid health probes, automated pool promotion/demotion with configurable thresholds, time-series cost analytics, and a full admin API surface. A contextual-bandit routing hook (Thompson Sampling) replaces the static weight formula — see [spec/agent.md](../agent.md).
