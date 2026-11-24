-- Per-service-token monthly USD budgets and month-to-date spend.
ALTER TABLE service_tokens ADD COLUMN IF NOT EXISTS monthly_budget_usd NUMERIC(12,4);   -- NULL = no budget

CREATE TABLE IF NOT EXISTS service_token_spend (
  service_token_id UUID NOT NULL REFERENCES service_tokens(id) ON DELETE CASCADE,
  month            DATE NOT NULL,          -- first day of the UTC month
  spend_micro_usd  BIGINT NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (service_token_id, month)
);
