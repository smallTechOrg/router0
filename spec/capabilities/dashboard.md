# Capability: Dashboard

## What It Does

Provides a Next.js web application where authenticated users manage provider keys, monitor pool health, review recent request metadata, and view estimated provider spend. Admins additionally access an invite management screen.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| JWT access token | HTTP cookie / Authorization header | Browser (NextAuth session) | Yes |
| User actions (add key, delete key, test key) | Form submissions | Browser | Contextual |
| Pool state, request feed, cost data | JSON | Router service internal API | Yes (fetched server-side) |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| Rendered HTML pages (SSR) | HTTP response | Browser |
| Mutating API calls | JSON HTTP requests | Router service API routes |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| Router Go service API | `GET /api/pool`, `GET /api/requests`, `GET /api/costs`, key CRUD | Show error banner; do not crash page |
| Router Go service API | `POST /api/keys`, `DELETE /api/keys/:id`, `POST /api/keys/:id/test` | Display inline error message |

## Business Rules

### Navigation Structure

```
/                     → redirect to /dashboard if authenticated, else /auth/login
/auth/login           → email + password login form
/auth/register        → registration form (requires invite token in URL query param)
/dashboard            → pool status overview (default landing after login)
/dashboard/keys       → provider key management
/dashboard/requests   → request feed (last 50 requests)
/dashboard/costs      → estimated cost tracker (Phase 1: table only, no charts)
/admin/invites        → admin only: create and list invites
```

### Authentication

NextAuth.js handles session management. Credentials provider uses the Router service's `POST /auth/login` endpoint to verify email/password and returns a JWT embedded in the NextAuth session. All `/dashboard/*` and `/admin/*` routes are server-side protected — unauthenticated requests redirect to `/auth/login`.

### Pool Status Page (`/dashboard`)

Displays two sections — **Active Pool** and **Common Pool** — each as a sortable table:

| Column | Source |
|--------|--------|
| Provider | `pool_scores.provider` |
| Model | `pool_scores.model` |
| Score | `pool_scores.score` (2 decimal places) |
| p95 Latency | `pool_scores.p95_latency_ms` ms |
| Error Rate | `pool_scores.error_rate_1h` % (1 hour window) |
| Quota (min / hr / day) | Redis rate-limit remaining counters |
| Last Health Check | `pool_scores.last_health_check_at` relative time |
| Status | Green / Yellow / Red indicator |

Status indicator logic:
- Green: score ≥ 0.70 AND error_rate_1h < 0.05
- Yellow: score ≥ 0.40 AND score < 0.70
- Red: score < 0.40 OR error_rate_1h ≥ 0.15

Refresh: auto-polls every 30 seconds via `setInterval` + `router.refresh()`.

### Provider Key Management Page (`/dashboard/keys`)

Lists all provider keys with: provider icon, label, key hint, status badge, last-tested timestamp, and actions (Test, Delete).

**Add key form:** provider dropdown, optional label, raw API key field (masked input). Submit calls `POST /api/keys`. On success, refreshes the list. The raw key input is cleared immediately after submission.

**Test button:** calls `POST /api/keys/:id/test`, shows a spinner, then inline result: `OK (234 ms)` or `Error: invalid_api_key`.

**Delete button:** confirmation modal ("This will remove this key from all pools. Existing request logs are retained."), then calls `DELETE /api/keys/:id`.

**Quota display:** for each key, a mini-row shows minute / hour / day quota remaining with a colour-coded bar (green > 50%, yellow 20–50%, red < 20%) and the reset countdown. Reset countdown is derived from the Redis TTL returned by the pool API.

### Request Feed Page (`/dashboard/requests`)

Table of the last 50 request metadata records (most recent first):

| Column | Source |
|--------|--------|
| Time | `request_metadata.created_at` relative |
| Model | `request_metadata.model` |
| Provider | `request_metadata.provider` |
| Input tokens | `request_metadata.input_tokens` |
| Output tokens | `request_metadata.output_tokens` |
| Latency | `request_metadata.latency_ms` ms |
| Cost (est.) | `request_metadata.estimated_cost_usd` formatted as $0.000000 |
| Status | success / error / hedged badge |

No pagination in Phase 1 (hard limit: last 50). Prompt content is never displayed. Auto-refreshes every 15 seconds.

### Cost Tracker Page (`/dashboard/costs`)

A table grouped by provider showing:

| Column | Value |
|--------|-------|
| Provider | |
| Requests this month | COUNT of `request_metadata` rows for current calendar month |
| Input tokens | SUM |
| Output tokens | SUM |
| Estimated cost | SUM of `estimated_cost_usd` |

Month resets at UTC midnight on the 1st. "This month" means current UTC calendar month.

Phase 1: table only. Charts (time-series spend over time) are a Phase 2 stub — the page shows a labelled placeholder: "Spend chart coming soon."

### Admin Invites Page (`/admin/invites`)

Visible only to users with `role = 'admin'` (enforced server-side). Lists all invites with email, status (pending/accepted/expired), created date, and expiry. A form at the top creates a new invite: email field + "Send Invite" button. In Phase 1, the invite token is displayed on-screen after creation (email delivery is out of scope).

### UI Stubs (Phase 1 — labelled non-functional)

The following surfaces exist in the UI but display a "Coming soon" label and no interactive controls:

- Spend chart on `/dashboard/costs`
- p95 latency / success rate time-series charts (1h / 24h / 7d) — referenced in the pool page under a "Analytics" heading with a stub card
- Force promote / demote controls in pool table (buttons present but disabled with tooltip "Available in a future release")

These stubs are never mistaken for bugs because they carry explicit "Coming soon" labels.

### Styling

Tailwind CSS. No component library — keep it minimal. Dark mode support via `prefers-color-scheme`.

### Error Handling

- API errors display a dismissable error banner at the top of the relevant section.
- Network failures during auto-refresh do not interrupt the user; they show a small "Last updated: N seconds ago" indicator.
- Form validation is client-side (HTML5 required attributes) and server-side (API returns 422 with field errors displayed inline).

## Success Criteria

- [ ] Visiting `/auth/login` while unauthenticated renders the login form without server error.
- [ ] Submitting valid credentials redirects to `/dashboard` and shows the pool status table.
- [ ] Visiting `/dashboard` with a user who has one active Groq key shows at least one pool row with a non-zero score.
- [ ] Adding a new provider key via the form, then refreshing the page, shows the new key in the list with a redacted key hint.
- [ ] Clicking "Test" on a valid key displays `OK (<N> ms)` inline within 10 seconds.
- [ ] Clicking "Delete" and confirming removes the key from the list and subsequent requests do not use it.
- [ ] The request feed at `/dashboard/requests` shows the most recent request after sending a chat completion via the API, with correct model, provider, latency, and token counts.
- [ ] The cost tracker at `/dashboard/costs` shows a non-zero estimated cost after at least one successful request.
- [ ] Visiting `/admin/invites` as a non-admin user is redirected to `/dashboard` (HTTP 302).
- [ ] The "Spend chart coming soon" stub is visible on the costs page and does not produce a JavaScript console error.
- [ ] `npm run build` in the `dashboard/` directory completes without errors.
