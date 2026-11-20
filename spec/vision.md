# Vision: router0

## What It Is

router0 is a self-hosted, multi-tenant, OpenAI-compatible LLM gateway that backend services call instead of calling LLM providers directly. The tenant is an **organisation (org)**. An org brings its own provider API keys (BYOK — several keys per provider are normal), sets one global provider priority order plus a key order inside each provider, and issues many **router0 service tokens** (`r0-sk-...`), one per backend service, each independently revocable.

A backend service sends a standard OpenAI request with a **canonical model name** (for example `claude-sonnet-5-5` or `llama-3.3-70b`). router0 resolves which of the org's providers and keys can serve that model, tries them in the org's fixed priority order, fails over on any error, and returns either the response (streamed or not) or one of three distinct, actionable OpenAI-style errors: `404 model_not_found`, `402 insufficient_credits`, or `429 rate_limited` with `Retry-After`.

Renamed from "Zer0 Router". The existing Go + Next.js + Postgres codebase is extended, not rewritten. PostgreSQL is the only backing store (see architecture.md › Stack).

## The Problem It Solves

Teams that run several backend services against several LLM providers hit four problems:

1. **Provider keys copied into every service.** Each service holds raw provider keys; rotating or revoking one means redeploying many services.
2. **Single-provider fragility.** A provider outage, a throttled key, or an empty credit balance breaks the product, and every service has to write its own failover.
3. **Inconsistent model names.** The same model is `llama-3.3-70b-versatile` on Groq and `meta-llama/llama-3.3-70b-instruct` on OpenRouter. Services hard-code provider-specific names and cannot fail over across providers.
4. **Opaque failures.** When a call fails, the service cannot tell "wrong model name" from "out of credits" from "slow down and retry", so it cannot react correctly.

router0 fixes all four: services hold only a revocable router0 token, ask for a canonical model, and get either an answer or an error code that says what to do next.

## Who It Is For

- **Primary:** Engineering teams running multiple backend services (APIs, workers, agents) that call LLMs, who already hold keys on two or more providers (Anthropic, OpenRouter, Groq, Gemini, Mesh API by Fiesta Labs) and want one internal endpoint with predictable failover.
- **Secondary:** Platform engineers who need to self-host the gateway (no prompt data leaves their infrastructure except to the providers they chose).

## What Success Looks Like

An org owner:
1. Registers (via admin invite), which creates their org, and adds provider keys in the dashboard: two Groq keys and one OpenRouter key.
2. Sets provider priority `groq → openrouter` and key order inside Groq.
3. Creates a service token `billing-worker` and pastes it into that service's config with `base_url=https://router0.internal/v1`.
4. The service calls `model="llama-3.3-70b"`. router0 serves it from the first Groq key; when that key gets throttled it fails over to the second Groq key, then to OpenRouter, without the service noticing.
5. When every key is throttled, the service gets `429 rate_limited` with `Retry-After: 12` and backs off for exactly that long.
6. The request log in the dashboard shows each request with the canonical model, the provider and key label that served it, latency, tokens, status, and the failover trail (`groq/key-A: rate_limit → groq/key-B: success`).
7. A leaked token is revoked in one click; the next call with it gets `401`.

## What It Is Not

- Not a prompt store: router0 never stores prompt or response content (metadata only).
- Not a smart or "best model" router: it never substitutes a different model for the one requested, and selection is fixed priority, not scoring.
- Not a billing or reselling layer: orgs pay providers directly; router0 adds no markup.
- Not a managed SaaS: it ships as a self-hosted Go binary plus a Next.js dashboard backed by PostgreSQL only, runnable bare-local on a developer machine.
- Not an evaluation, fine-tuning, or model-hosting tool.
- Not a conversation history store: callers own their message history; every request is stateless.

## Scale Target

One Go binary handles the request path. PostgreSQL is the only shared store (tokens, key cooldown state, catalog, logs), and short-lived caches live in process memory. Single instance is the target; more instances behind a load balancer stay correct because Postgres is the source of truth, with bounded cache staleness. Routing adds negligible overhead: a cached token lookup, one catalog query, one key-state query, and in-memory ordering. No LLM call happens inside router0's decision path.

Assumed: target is tens of services and up to ~200 requests/second per instance, which is well within a single Go process. No sharding is designed.
