CREATE TABLE orgs (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE org_members (
  org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, user_id)
);
CREATE UNIQUE INDEX org_members_one_org_per_user ON org_members(user_id);

ALTER TABLE invites
  ADD COLUMN org_id   UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN org_role TEXT CHECK (org_role IN ('admin','member'));

-- Backfill: one org per existing user (named after the email); the user is owner.
-- Idempotent: only users that are not yet a member of any org.
DO $$
DECLARE
  u RECORD;
  new_org UUID;
BEGIN
  FOR u IN
    SELECT id, email FROM users
    WHERE NOT EXISTS (SELECT 1 FROM org_members m WHERE m.user_id = users.id)
    ORDER BY created_at, id
  LOOP
    INSERT INTO orgs (name) VALUES (u.email) RETURNING id INTO new_org;
    INSERT INTO org_members (org_id, user_id, role) VALUES (new_org, u.id, 'owner');
  END LOOP;
END $$;
