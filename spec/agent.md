# AI-Native Design: router0

## Framing

router0 is infrastructure that LLM-powered backend services (many of them agents) call. The AI-native questions are therefore:

1. Should any decision inside router0 be made by a model (an LLM, embeddings, or a learned policy)?
2. What does router0 have to do so that AI callers — agents, tool-using workers, SDKs — work well through it?

Short answer: **no model reasoning happens inside router0. Routing, name resolution, and error classification are deterministic.** The AI-native work goes into being a faithful substrate for AI callers: exact OpenAI wire compatibility, correct tool-calling translation, streaming, and machine-actionable errors.

## Decisions Inside router0

### 1. Model selection / "smart routing" — Rejected

**Considered:** pick the provider or even the model per request from prompt content (semantic routing), learned quality, or cost/latency scoring (the old weighted scorer and planned Thompson-sampling bandit).

**Decision:** Fixed priority only. The user-approved brief requires one global provider order per org, key order within each provider, and a per-request header override. Operators need to predict which key is billed and why. A learned or semantic policy would make the billing key unpredictable, add latency on the hot path, and could substitute a model the caller did not ask for. The old `ScoreFunc`/bandit hook from the Zer0 spec is retired; the legacy scorer and pool code are taken off the request path in Phase 1 and deleted in Phase 3.

### 2. Model-name normalisation with an LLM or embeddings — Rejected

**Considered:** when a caller sends a name router0 does not know (`llama3.3-70B`, `sonnet latest`), ask an LLM or nearest-neighbour embeddings to guess the closest catalog entry.

**Decision:** Deterministic rules plus explicit aliases. A fuzzy match that is wrong sends traffic to a different model without anyone noticing. That is a correctness and cost hazard, and it cannot be reproduced when debugging. The deterministic path is:
- `catalog.Canonicalize`: lowercase, strip vendor prefix, strip known suffixes and date stamps, family-specific rules, and a static override table (capabilities/model-catalog.md);
- org aliases defined by humans (Phase 2);
- on-demand discovery: on a miss, re-list the org's providers and match with the same rules (Phase 2).
If none of these match, the caller gets `404 model_not_found` with the requested name, which they can fix right away.

### 3. Error classification with an LLM — Rejected

**Considered:** read free-text provider error bodies with a model to decide whether a failure means "rate limited", "out of credits", or "transient".

**Decision:** Rule tables per provider, keyed on HTTP status, structured error `code`/`type` fields, and a short list of documented message substrings (capabilities/error-classification.md). Provider errors are structured enough. A model call on the failure path would add latency exactly when the system is degraded, and could misclassify, which changes what the caller is told (402 vs 429). Rules are unit-tested against recorded fixtures.

### 4. LLM-as-judge / response quality feedback — Rejected

router0 never reads or stores response content (binding rule), so it cannot judge quality. Callers who need evaluation do it in their own service.

### 5. Agentic retry policy — Rejected

The failover policy is a finite loop over an ordered candidate list with explicit skip states and a fixed error precedence (429 > 402 > 404 > 400 > 503). An agent cannot make this faster or more reliable, and it would make failures non-reproducible.

### 6. Prompt-injection or content filtering — Not applicable

router0 does not interpret message content; nothing in routing depends on it. Content filtering, if wanted, belongs to the caller or the provider.

### 7. Natural-language analytics in the dashboard — Deferred, not planned

Asking "why did billing-worker fail last night?" in natural language is a nice-to-have. The request log's filters plus the per-request failover trail answer it directly. Not in Phases 1–3.

## What router0 Does for AI Callers

These are requirements, written as success criteria in the capability files:

| Need of an AI caller | router0 behaviour | Where specified |
|----------------------|-------------------|-----------------|
| Drop-in SDK compatibility | OpenAI request/response/SSE shapes, OpenAI error shape (`error.type/code/param`) so SDK exception classes map correctly (404 → `NotFoundError`, 429 → `RateLimitError`) | api.md |
| Correct tool/function calling across providers | Pass-through for OpenAI-compatible providers (Phase 1); full translation to Anthropic `tool_use`/`tool_result` blocks, `tool_choice` mapping, streamed `input_json_delta` → `tool_calls[].function.arguments` deltas (Phase 2); Gemini conformance tests (Phase 2) | provider-adapters.md |
| Self-directed backoff | `429` with exact `Retry-After` from the soonest key reset; distinct `402` that tells the agent "retrying will not help" | error-classification.md |
| Stable model identity | Canonical names stable across providers; `model` in responses echoes the canonical name | model-catalog.md |
| Reliable streams | Failover before first byte; a clear in-band `stream_interrupted` error after it, never a silent truncation | routing-engine.md |
| Discoverability | `GET /v1/models` lists exactly what the org can call | api.md |

## Conclusion

router0 contains **no LLM, embedding, or learned-policy component** in any phase. Every routing decision is deterministic: canonical name → org catalog → fixed provider priority (or header override) → key order → skip keys in cooldown → fail over on any error → fixed error precedence. This matches the user's explicit choice of fixed priority, keeps billing predictable, keeps the hot path sub-millisecond apart from I/O, and makes every outcome reproducible from the request log trail. The AI-native investment is in fidelity for AI callers: wire compatibility, tool-calling translation, streaming behaviour, and actionable error codes.
