# Capability: Credit Tracking (Balance Polling, Monthly Budgets)

Phase 3. Phase 1 already detects credit exhaustion reactively (402 / credit error codes → `provider_key_state` row `credits_exhausted`, see error-classification.md). This capability adds proactive detection.

## What It Does

1. **Balance polling:** for providers with a documented balance or limit API, periodically read each key's remaining balance and mark exhausted keys before a request fails.
2. **Monthly budgets:** an optional USD budget per key. router0 tracks the key's month-to-date spend from its own usage records and stops using the key when the budget is reached.

## Inputs

| Input | Source |
|-------|--------|
| OpenRouter `GET {OPENROUTER_BASE_URL}/key` → `data.limit`, `data.usage`, `data.limit_remaining`; `GET /credits` → `data.total_credits`, `data.total_usage` | OpenRouter API |
| `provider_keys.monthly_budget_usd` | `PATCH /api/keys/{id}` |
| `estimated_cost_usd` of each successful request | Metadata recorder |

## Outputs

| Output | Destination |
|--------|-------------|
| `provider_key_balances` rows | PostgreSQL |
| `provider_key_state` `credits_exhausted` row set/cleared with reason `balance_poll` | PostgreSQL |
| `provider_key_spend (key, month)` micro-USD | PostgreSQL |
| `budget_exhausted` key state | keystate → routing (skip) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| OpenRouter `/key`, `/credits` | Poll every 10 min per active OpenRouter key (`internal/balance/`) | `status='error'`; keep the last value; no key state change |
| Other providers | — | `status='unsupported'`. Assumed: Groq, Anthropic, Gemini, NVIDIA, Together, and Baseten have no public per-key balance API at spec time. Mesh API is checked during the slice; if no balance API is documented, it is `unsupported` |
| PostgreSQL | `UPDATE provider_key_spend … += ` (upsert); budget read in keystate via a 5 s in-memory cache | Spend write failure: log `error`, the hourly reconcile repairs it. Read failure: fail open |

## Business Rules

- **Polling:** the remaining balance for OpenRouter is `limit_remaining` when the key has a limit, else `total_credits − total_usage`. If ≤ 0 → upsert a `credits_exhausted` row (`until_at` = now + poll interval + 60 s, reason `balance_poll`). If > 0 and the existing credits state came from `balance_poll`, delete it. A credits state set by a live 402 is left to expire by itself. Assumed: a live 402 is more authoritative than a poll.
- **Spend:** on each successful request with a cost > 0, the recorder runs `INSERT INTO provider_key_spend … ON CONFLICT (provider_key_id, month) DO UPDATE SET spend_micro_usd = provider_key_spend.spend_micro_usd + EXCLUDED.spend_micro_usd` with `round(cost × 1e6)`, in the same async write as the request row. Hourly, and on startup, it reconciles the current month from `SUM(estimated_cost_usd)` of `request_metadata` (the request log is authoritative).
- **Budget check:** `keystate.Check` also returns `budget_exhausted` when `monthly_budget_usd` is not null and spend ≥ budget. The key is skipped and counts as `insufficient_credits` in the final-error precedence (→ 402). The budget resets at 00:00 UTC on the 1st of the month (new `month` row).
- Budgets are estimates based on router0's cost table and catalog pricing. The dashboard shows "estimated".
- The keys page shows balance (if supported), month-to-date spend, and budget.

## Success Criteria

- [ ] Mock OpenRouter `/key` returns `limit_remaining: 0` → within one poll cycle (test hook: poll now), the key shows `credits_exhausted` and requests skip it without calling chat.
- [ ] The same mock then returns `limit_remaining: 5` → the next poll clears the state.
- [ ] A key with `monthly_budget_usd = 0.001` and mock pricing that makes each request cost $0.0006: the 1st and 2nd requests are served by it, the 3rd is skipped with `budget_exhausted`, and with no other key the client gets 402.
- [ ] After `DELETE FROM provider_key_spend`, reconciliation restores the spend counter from `request_metadata` (within 1 µUSD).
- [ ] `PATCH /api/keys/{id} {"monthly_budget_usd": null}` removes the limit and the key is used again.
