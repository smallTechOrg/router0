DROP TABLE IF EXISTS provider_key_spend;
DROP TABLE IF EXISTS provider_key_balances;
ALTER TABLE provider_keys DROP COLUMN IF EXISTS monthly_budget_usd;
