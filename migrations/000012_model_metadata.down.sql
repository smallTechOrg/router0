ALTER TABLE provider_models
    DROP COLUMN IF EXISTS parameter_count_b,
    DROP COLUMN IF EXISTS architecture;
