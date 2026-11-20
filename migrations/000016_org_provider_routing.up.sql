ALTER TABLE provider_keys
  ADD COLUMN org_id   UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN position INT NOT NULL DEFAULT 0;

UPDATE provider_keys k SET org_id = m.org_id
FROM org_members m
WHERE m.user_id = k.user_id AND k.org_id IS NULL;

-- Position backfill: order within (org, provider) by creation time.
UPDATE provider_keys k SET position = r.rn - 1
FROM (
  SELECT id, row_number() OVER (PARTITION BY org_id, provider ORDER BY created_at, id) AS rn
  FROM provider_keys
) r
WHERE r.id = k.id;

ALTER TABLE provider_keys ALTER COLUMN org_id SET NOT NULL;

CREATE INDEX provider_keys_org_provider_pos ON provider_keys(org_id, provider, position);
CREATE UNIQUE INDEX provider_keys_org_provider_label ON provider_keys(org_id, provider, label) WHERE status <> 'disabled';

CREATE TABLE org_provider_priority (
  org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider   TEXT NOT NULL,
  position   INT  NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, provider),
  UNIQUE (org_id, position)
);

-- Backfill: per org, distinct providers of non-disabled keys, alphabetical, positions 0..n-1.
INSERT INTO org_provider_priority (org_id, provider, position)
SELECT org_id, provider, row_number() OVER (PARTITION BY org_id ORDER BY provider) - 1
FROM (
  SELECT DISTINCT org_id, provider FROM provider_keys WHERE status <> 'disabled'
) d
ON CONFLICT DO NOTHING;
