# AI-Native Design: Zer0 Router

## Evaluation Summary

The routing engine is the primary intelligence surface in Zer0 Router. This file evaluates every applicable AI/agent pattern from the catalogue against the routing problem and records the design decision.

## Pattern Evaluation

### 1. Retrieval-Augmented Generation (RAG)

**Opportunity considered:** Retrieve provider status pages or community-reported incidents to inform routing decisions.

**Decision: Not applicable.** Provider health is tracked directly from live response metadata (latency, error rate, rate-limit headers) — real-time first-party signals that are strictly more accurate and lower-latency than retrieved external documents. Adding a retrieval step would increase latency on the critical path with no accuracy gain. Rationale: routing signals are numerical metrics, not natural-language knowledge.

### 2. LLM-as-Judge / Quality Evaluation

**Opportunity considered:** Use a fast LLM to evaluate response quality and feed that signal back into pool scoring.

**Decision: Not applicable in Phase 1; deferred to Phase 2+.** Quality evaluation requires calling a second LLM per request, doubling cost and adding latency. In Phase 1, error rate and provider HTTP status codes are sufficient correctness proxies. A quality feedback loop is a valid Phase 3 feature if users request it.

### 3. Chain-of-Thought / Structured Reasoning

**Opportunity considered:** Use CoT to select among routing options.

**Decision: Not applicable.** Routing is a deterministic weighted scoring formula over numeric metrics. There is no ambiguity requiring reasoning — the formula produces a total ordering of combos in microseconds. CoT would add hundreds of milliseconds of latency and cost per request with no benefit over the formula.

### 4. Agent with Tools

**Opportunity considered:** An agent that adaptively decides whether to retry, hedge, or fail based on rich context.

**Decision: Not applicable.** The retry/hedge policy is a finite state machine (try top combo → on failure, try next → on second failure, hedge two simultaneously). The states and transitions are fully specified and deterministic. An LLM agent cannot make this decision faster or more reliably than the FSM, and adding one would make failure modes harder to reason about.

### 5. Contextual Bandit / Reinforcement Learning (Phase 2 hook)

**Opportunity considered:** Replace the static scoring weights with a learned policy that optimises routing for each user's traffic pattern.

**Decision: Deferred to Phase 2, with a concrete hook point designed in Phase 1.**

The static scoring formula in Phase 1 uses fixed weights (0.35 / 0.30 / 0.20 / 0.15) chosen by domain knowledge. These weights are optimal for average traffic but may not be optimal for individual users. A contextual bandit can learn per-user optimal weights from historical request metadata stored in PostgreSQL.

**Phase 2 plan:**

- **Algorithm:** Thompson Sampling over the discrete action space of `(provider, model)` combos per user. Each combo maintains a Beta distribution `Beta(α, β)` where α = success count and β = failure count in a rolling time window.
- **Context features:** time of day, estimated request token count (from input length), model preference in the original request (if provided).
- **Hook point (Phase 1 scaffolded):** `internal/routing/scorer.go` exposes a `ScoreFunc` interface:

  ```go
  type ScoreFunc interface {
      Score(ctx context.Context, userID string, combos []Combo, metrics map[string]ComboMetrics) []ScoredCombo
  }
  ```

  Phase 1 implements `WeightedScoreFunc` (the static formula). Phase 2 implements `BanditScoreFunc` that queries the Thompson Sampling state from Redis and samples from the Beta distributions.

- **State storage:** Alpha/beta counts stored in Redis hash `bandit:{user_id}:{provider}:{model}` — fast read at request time, updated async after each request outcome.
- **Cold start:** Fall back to `WeightedScoreFunc` until a combo has ≥ 20 observations.
- **Switchover:** Feature-flagged via `ROUTING_STRATEGY` env var (`weighted` | `bandit`); defaults to `weighted` in Phase 1 and Phase 2.

### 6. Embedding-Based Semantic Routing

**Opportunity considered:** Route requests to different models based on semantic similarity of the prompt to each model's known strengths (e.g. code → Llama, creative → Gemini).

**Decision: Not applicable in Phase 1.** This requires embedding the prompt (adding latency + cost) and a curated semantic profile per model (which does not exist in a structured, reliable form). The user's application already selects a model in the request; overriding it silently would violate the principle of least surprise. Deferred as a potential Phase 3 "smart model selection" feature if users request it.

### 7. Prompt Injection / Safety Filtering

**Opportunity considered:** Inspect prompts for injection attempts targeting the routing logic.

**Decision: Not applicable.** The router is stateless with respect to prompt content — it reads the message array to count tokens (for cost estimation) but does not interpret it. There is no routing decision that can be influenced by prompt content. No filtering is needed on this path.

## Conclusion

**No AI capability is needed or used in Phase 1 routing.** The routing engine is a deterministic weighted scoring formula (`internal/routing/scorer.go`) applied to numeric telemetry. This is not a simplification — it is the correct design: the problem is an optimisation over real-time numeric signals, not a reasoning or generation problem. Introducing an LLM would add latency, cost, and failure modes with no upside.

The single Phase 2 AI hook — a contextual bandit (Thompson Sampling) replacing the static weight formula — is scaffolded in Phase 1 via the `ScoreFunc` interface so it can be swapped in without touching the request handler or retry logic.

No `spec/agent.md` placeholder remains: this file is the complete AI-native design.
