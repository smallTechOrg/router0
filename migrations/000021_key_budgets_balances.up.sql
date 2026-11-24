-- Phase 3: per-key monthly USD budgets, polled provider balances, month-to-date spend.
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS monthly_budget_usd NUMERIC(12,4);   -- NULL = no budget

CREATE TABLE IF NOT EXISTS provider_key_balances (
  provider_key_id UUID PRIMARY KEY REFERENCES provider_keys(id) ON DELETE CASCADE,
  org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  remaining_usd   NUMERIC(14,6),          -- NULL = provider reports unlimited
  limit_usd       NUMERIC(14,6),
  usage_usd       NUMERIC(14,6),
  status          TEXT NOT NULL,          -- ok | exhausted | error | unsupported
  polled_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS provider_key_spend (
  provider_key_id UUID NOT NULL REFERENCES provider_keys(id) ON DELETE CASCADE,
  month           DATE NOT NULL,          -- first day of the UTC month
  spend_micro_usd BIGINT NOT NULL DEFAULT 0,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider_key_id, month)
);
