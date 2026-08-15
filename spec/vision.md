# Vision: Zer0 Router

## What It Is

Zer0 Router is a multi-tenant, OpenAI-compatible inference gateway. Users bring their own LLM provider API keys and receive a single `zer0:sk-xxx` token that acts as a drop-in replacement for the OpenAI API key. The router accepts standard OpenAI-format requests and silently proxies them to the best available provider on the user's behalf, streaming the response back unchanged.

## The Problem It Solves

Developers and teams that run LLM workloads at any meaningful volume face three compounding problems:

1. **Single-provider fragility.** One provider goes down or throttles you, and your product breaks.
2. **Cost inefficiency.** Different providers price the same model differently, and no single provider is cheapest across all models and traffic patterns.
3. **Key management sprawl.** Maintaining direct integrations with multiple providers multiplies API-key management, error handling, and monitoring surfaces.

Zer0 Router collapses these three problems into one: your application talks to one endpoint, one key, and the router handles provider diversity, failover, and cost optimisation behind the scenes.

## Who It Is For

- **Primary:** Individual developers and small engineering teams building LLM-powered products who are already using (or planning to use) multiple inference providers and want resilience without bespoke routing logic.
- **Secondary:** Platform engineers evaluating self-hosted inference gateways before committing to a managed service.

## What Success Looks Like

A user:
1. Registers (via admin invite), adds their provider API keys in a web dashboard, and copies their `zer0:sk-xxx` token.
2. Changes one line in their application — the base URL and the API key — and continues using the standard OpenAI client library.
3. Requests are served with no change in response format. Response headers reveal which provider and model were used.
4. When a provider fails or exhausts its rate limit, the router retries the next-best option transparently.
5. The dashboard shows live pool health, per-request metadata, and estimated spend, without ever storing prompt content.

## What It Is Not

- Not a prompt management or evaluation tool.
- Not a conversation history store (callers own their message history; the router is stateless per-request).
- Not a fine-tuning or model-hosting platform.
- Not a managed service in Phase 1 — it ships as a self-hosted binary backed by PostgreSQL and Redis.
- Not monetised in Phase 1 — users pay their providers directly; Zer0 Router adds no markup.

## Scale Target

The architecture is designed to scale to OpenRouter-level traffic. Phase 1 ships as a single deployable Go binary with Redis-shared state so that horizontal scaling (adding instances behind a load balancer) requires no code changes in Phase 2.
