DROP TABLE IF EXISTS org_model_aliases;

-- Rows with kinds the original CHECK does not allow (image/audio/other) are
-- DELETED (data.md); the next catalog refresh re-creates them as needed.
DELETE FROM org_model_catalog WHERE kind NOT IN ('chat','embedding');

DO $$ DECLARE c TEXT; BEGIN
  FOR c IN SELECT conname FROM pg_constraint
   WHERE conrelid = 'org_model_catalog'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%kind%'
  LOOP
    EXECUTE format('ALTER TABLE org_model_catalog DROP CONSTRAINT %I', c);
  END LOOP;
END $$;
ALTER TABLE org_model_catalog ADD CONSTRAINT org_model_catalog_kind_check
  CHECK (kind IN ('chat','embedding'));
