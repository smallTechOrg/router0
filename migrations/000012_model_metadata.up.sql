ALTER TABLE provider_models
    ADD COLUMN IF NOT EXISTS parameter_count_b numeric(10,2),
    ADD COLUMN IF NOT EXISTS architecture      text;
