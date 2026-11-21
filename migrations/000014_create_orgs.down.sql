ALTER TABLE invites DROP COLUMN IF EXISTS org_role,
                    DROP COLUMN IF EXISTS org_id;
DROP INDEX IF EXISTS org_members_one_org_per_user;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS orgs;
