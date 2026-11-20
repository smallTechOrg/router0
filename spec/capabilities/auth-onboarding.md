# Capability: Auth & Onboarding

## What It Does

Manages invite-only user registration, email/password authentication, and issuance of a single `zer0:sk-xxx` API key that the user presents on every inference request.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| Invite token | String (UUID) | Admin-created invite email | Yes (registration only) |
| Email | String | User form | Yes |
| Password | String | User form | Yes (min 12 chars) |
| Zer0 API key (`zer0:sk-xxx`) | String | User's HTTP `Authorization: Bearer` header | Yes (inference requests) |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| JWT access token (15 min TTL) | String | HTTP response body → client |
| JWT refresh token (30 day TTL) | String | HTTP-only cookie |
| Zer0 API key (full, shown once) | String | Registration response body |
| User record | PostgreSQL `users` row | Database |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | INSERT user, INSERT/UPDATE invite | Return 500; log structured error |
| PostgreSQL | SELECT user by email for login | Return 401 on mismatch |

## Business Rules

- Registration is invite-only. An invite is a single-use token (UUID) with a 7-day expiry. Accepting an invite marks it consumed; presenting an already-consumed or expired token returns HTTP 409.
- Password is bcrypt-hashed (cost 12) before storage. Password is never logged or returned.
- The Zer0 API key format: `zer0:sk-` prefix followed by 32 cryptographically-random base58 characters, e.g. `zer0:sk-3fGhJ8kLmNpQrStUvWxYzAb`. The full key is returned exactly once at registration and never stored in plaintext. The database stores a bcrypt hash (cost 12) of the key and a non-secret display prefix (first 12 chars after `zer0:sk-`) for dashboard identification.
- Each user has exactly one Zer0 API key at a time. Key rotation issues a new key and immediately invalidates the previous hash.
- Authentication for inference requests: extract the `Authorization: Bearer zer0:sk-xxx` header, bcrypt-compare against all active key hashes for the resolved user. Lookup is by the display prefix stored in plaintext (non-secret) to avoid a full-table scan, then bcrypt comparison.
- Authentication for dashboard API routes: JWT (HS256, secret from env `JWT_SECRET`). JWTs do not contain the Zer0 key or provider keys.
- Admin role is assigned directly in the database (`users.role = 'admin'`). No self-service admin promotion.
- A disabled user (`users.status = 'disabled'`) is rejected at both JWT validation and Zer0-key lookup.
- No OAuth in Phase 1. The registration/login form is the only onboarding path.

## Success Criteria

- [ ] `POST /auth/register` with a valid, unused invite token, email, and password returns HTTP 201, body contains `zer0_key` (full key, once), and the user row appears in PostgreSQL with `password_hash` populated and `zer0_key` absent.
- [ ] A second `POST /auth/register` with the same invite token returns HTTP 409.
- [ ] An expired invite token (manually backdated in DB) returns HTTP 410.
- [ ] `POST /auth/login` with correct credentials returns HTTP 200 with a JWT access token.
- [ ] `POST /auth/login` with wrong password returns HTTP 401; no timing difference observable at >10 ms granularity (constant-time compare).
- [ ] A valid Zer0 API key in the `Authorization: Bearer` header on `POST /v1/chat/completions` resolves to the correct user; an invalid key returns HTTP 401.
- [ ] A disabled user's Zer0 key returns HTTP 403.
- [ ] `POST /admin/invites` by an admin user creates an invite row and returns the invite token; the same call by a non-admin returns HTTP 403.
