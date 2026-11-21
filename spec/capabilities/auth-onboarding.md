# Capability: Orgs, Auth & Onboarding

Phase 1.

## What It Does

Turns the existing single-user accounts into members of an **org** (the tenant). Keeps invite-only registration and email/password login for humans using the dashboard. Every dashboard API call is scoped to the caller's org. Service-to-service auth is a separate capability (service-tokens.md).

## Inputs

| Input | Type | Source | Required |
|-------|------|--------|----------|
| Invite token | UUID string | Invite link created by platform admin | Registration |
| Email, password (≥12 chars) | strings | Register / login form | Yes |
| Org name | string ≤ 80 chars | Register form | No (default = email) |
| JWT access token | HS256 string | `Authorization: Bearer` on `/api/*` | Dashboard API |

## Outputs

| Output | Destination |
|--------|-------------|
| `users`, `orgs`, `org_members` rows | PostgreSQL |
| JWT access token (15 min) with claims `sub`, `email`, `role`, `org_id` | Response body → NextAuth session |
| Refresh token (30 days) | HTTP-only cookie (unchanged) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | Transaction: INSERT user, INSERT org (or look up invite org), INSERT org_member, UPDATE invite | Roll back; 500 `internal_error/db_error` |

## Business Rules

- Registration stays invite-only. Platform admins (`users.role='admin'`) create invites via `POST /admin/invites`.
- An invite with no `org_id` creates a new org on registration and makes the registrant `owner`. An invite with `org_id` adds the registrant to that org with `invites.org_role` (default `member`).
- Assumed: one org per user (unique index on `org_members.user_id`). Existing users are backfilled into one personal org each, as `owner` (migration 000014).
- Registration, org creation, membership, and invite consumption happen in one DB transaction (today the invite update is a separate, unchecked statement; that is fixed).
- Registration no longer generates a `zer0:sk-` key. The response carries `org` and `access_token`. The dashboard then sends the user to create a service token.
- Org roles: `owner` and `admin` may write (keys, priority, tokens, catalog refresh, aliases); `member` is read-only. Enforced by `auth.RequireOrgRole("owner","admin")` on every write route under `/api/*`.
- `JWTMiddleware` puts `org_id` in the context. When an older JWT has no `org_id` claim, the middleware loads it from `org_members`. A user with no membership gets `403 forbidden/no_org`.
- Platform admin bootstrap (`ADMIN_EMAIL`/`ADMIN_PASSWORD` in `main.go`) also creates the admin's org if missing.
- A disabled user (`users.status='disabled'`) is rejected at login and at JWT validation. Disabling a user does not revoke the org's service tokens. Tokens belong to the org, not the person. Assumed.
- Passwords: bcrypt cost 12, never logged (unchanged).

## Success Criteria

- [ ] `POST /auth/register` with a valid invite (no `org_id`) returns 201 with `org.role = "owner"`; `orgs`, `org_members` rows exist; the response has no `zer0_key` field.
- [ ] Registering with an invite carrying `org_id = X` and `org_role = member` makes the user a `member` of X; that user's `POST /api/tokens` returns 403 `org_role_required`, and `GET /api/tokens` returns 200.
- [ ] A reused invite returns 409; an expired invite returns 410; a 10-char password returns 422.
- [ ] `GET /auth/me` returns the `org` object.
- [ ] User in org A calling `GET /api/keys/{id}` or `DELETE /api/tokens/{id}` for org B's resource gets 404.
- [ ] After migration 000014 on a DB holding pre-existing users, every user has exactly one `org_members` row with role `owner`.
- [ ] A JWT issued without an `org_id` claim still works on `/api/keys` (org resolved from membership).
