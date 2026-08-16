ALTER TABLE provider_models
    ALTER COLUMN input_cost_per_mtok  TYPE NUMERIC(14,8),
    ALTER COLUMN output_cost_per_mtok TYPE NUMERIC(14,8);
