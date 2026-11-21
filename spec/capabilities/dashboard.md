# Capability: Dashboard

Phase 1: rename to router0, tokens page, org-scoped keys with order and state, routing priority page, request log with failover trail, overview. Phase 2: catalog page and aliases. Phase 3: drag-and-drop priority, key budgets and balances, reset key state.

## What It Does

The Next.js 14 app where org members manage router0: service tokens, provider keys, provider priority and key order, model catalog, and the request log.

## Inputs

| Input | Source |
|-------|--------|
| NextAuth session holding the Go-issued JWT (`org_id` claim) | `dashboard/lib/auth.ts` |
| Go API (`/api/*`, `/auth/*`, `/admin/*`) via rewrites to `:8080` | `dashboard/lib/api.ts` |

## Outputs

Pages, plus API calls for writes. No data is stored in the browser except NextAuth cookies.

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| Go gateway | typed fetch wrappers in `lib/api.ts` | Inline red error banner with `error.message`; 401 → redirect to `/auth/login` |

## Business Rules

### Navigation (sidebar, brand "router0")

`Overview` (`/dashboard`) · `Tokens` · `Provider Keys` · `Routing` · `Requests` · `Catalog` (P2) · `Costs` · `Admin` (platform admins only: invites, users/orgs, legacy models).

### Overview (`/dashboard`) — Phase 1

Replaces the pool-status page. Cards: live tokens, active keys by provider, requests and error rate in the last 24 h (from `/api/requests`), and a copyable quick-start snippet:
```
curl $ROUTER0_URL/v1/chat/completions -H "Authorization: Bearer r0-sk-…" \
  -H "Content-Type: application/json" -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}'
```
A new user with no tokens sees a "Create your first service token" call-to-action linking to Tokens.

### Tokens (`/dashboard/tokens`) — Phase 1

Table: name, prefix (`r0-sk-XXXXXXXXXXXX…`), created by, created, last used, status (`live` / `revoked` / `legacy`). The "Create token" form takes a name. On success a modal shows the full token once, with a copy button and the text "This token will not be shown again." Each live row has a "Revoke" button with a confirm dialog. Write controls are hidden for `member` role.

### Provider Keys (`/dashboard/keys`) — Phase 1

Grouped by provider in priority order. Per key: label, hint, status badge (`active` green / `error` red / state badges: `rate limited · 12s` amber, `out of credits` red, `cooling down` grey), last test result, and buttons ▲ ▼ (key order; each click sends `PUT /api/routing/priority` with the new `key_order`), Test, and Remove. The add-key form has a provider select (from the registered providers; labels "Groq", "OpenRouter", "Gemini", "Anthropic", "Mesh API (Fiesta Labs)", …), the key, and a label. The page polls `GET /api/keys` every 10 s so state badges expire visibly.

### Routing (`/dashboard/routing`) — Phase 1

An ordered list of providers that have keys, with ▲ ▼ buttons and Save (`PUT /api/routing/priority`). A help box explains the order and the `X-Router0-Providers` header override, with an example. P3: drag-and-drop replaces the arrows (arrows kept for keyboard accessibility).

### Requests (`/dashboard/requests`) — Phase 1

Table (newest first, 50 per page, "Load older" uses `before`): time, token, canonical model, provider, key label, status (badge with HTTP code and `error_code`), latency, tokens in/out, attempts. Clicking a row expands the failover trail as a horizontal chain: `groq/groq-a ✕ rate_limit 429 (95 ms) → groq/groq-b ✓ (717 ms)`. Skipped candidates show `⤼ skipped: rate_limited 9s`. Filters: status, model, token. An auto-refresh toggle (10 s).

### Catalog (`/dashboard/catalog`) — Phase 2

Search box (`?q=`) and a **kind filter** (`all | chat | image | audio | embedding | other`, default `chat`; calls `GET /api/catalog?kind=`). A table of canonical model → provider chips (`groq · llama-3.3-70b-versatile`, `meshapi · mistral/mistral-7b-instruct-v0`), with unavailable ones struck through, plus kind and last seen. Non-chat kinds are shown for visibility only and carry a note "not served by router0". A "Refresh now" button (`POST /api/catalog/refresh`) shows the per-provider refresh status and time. Aliases section: list, add (alias → canonical, with autocomplete from the catalog), delete.

### Costs (`/dashboard/costs`) — existing, org-scoped; P3 adds per-key month-to-date spend vs budget bars.

### Admin (`/dashboard/admin`) — existing; invite form gains an optional "Join existing org" select.

### Styling and errors

Tailwind, the existing dark neutral palette and `StatusBadge` component. All writes show a success toast or an inline error. No page crashes on a failed fetch.

## Success Criteria

- [ ] `pnpm run build` succeeds with zero type errors.
- [ ] No visible "Zer0" / "zer0" string remains in `dashboard/app` or `dashboard/components` (`grep -ri zer0 dashboard/app dashboard/components` is empty).
- [ ] Manual: create a token → modal shows the full `r0-sk-…` once; reload → only the prefix shows; revoke → status `revoked`.
- [ ] Manual: two Groq keys; ▼ on the first swaps them; reload keeps the order; the next request's trail starts with the new first key.
- [ ] Manual: Routing page reorder + Save → the next request tries the new first provider.
- [ ] Manual: after a failover request, Requests shows attempts = 2 and the expanded trail names both keys with outcome and class.
- [ ] Manual: a `member` user sees no Create / Revoke / Add / Remove / Save controls.
- [ ] (P2) Catalog page: kind filter `image`/`audio` lists the Gemini image/TTS/music mock models and the default `chat` view does not; "Refresh now" updates the per-provider status; it lists `llama-3.3-70b` with both providers; adding alias `fast` shows it in the list and in `/v1/models`.
