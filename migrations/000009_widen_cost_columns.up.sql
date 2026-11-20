-- Widen cost columns to handle high-precision pricing (some OpenRouter models have very large cost values)
ALTER TABLE provider_models
    ALTER COLUMN input_cost_per_mtok  TYPE NUMERIC(20,8),
    ALTER COLUMN output_cost_per_mtok TYPE NUMERIC(20,8);
