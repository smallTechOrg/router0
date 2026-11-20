# Capability: Model Catalog

Phase 1: auto-discovery for Groq and OpenRouter, canonical names, `/v1/models`, refresh API. Phase 2: discovery for Anthropic, Gemini, and Mesh API, org aliases, on-demand discovery, catalog page. Phase 3: embedding-model discovery.

## What It Does

Builds, per org, a map from **canonical model name** to the providers (and provider-specific model ids) that the org's keys can actually reach. Callers ask for the canonical name, and the routing engine asks the catalog which providers serve it.

## Inputs

| Input | Source |
|-------|--------|
| Provider `/models` listings via `Provider.ListModels(ctx, apiKey)` | Each provider's API, called with the org's first usable key |
| Requested model string | `POST /v1/chat/completions` body |
| Aliases (P2) | `org_model_aliases` |

## Outputs

| Output | Destination |
|--------|-------------|
| `org_model_catalog` rows, `org_catalog_refreshes` rows | PostgreSQL |
| `[]catalog.Entry` for a requested model | Routing engine |
| `/v1/models` list, `/api/catalog` view | HTTP |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| Provider `GET {base}/models` | List models (Groq, OpenRouter in P1; Anthropic `GET /v1/models` paginated, Gemini `GET /v1beta/models`, Mesh API `GET /v1/models` in P2) | Record `last_status='error'` with class; keep existing rows; on `auth` mark key `error` and try the provider's next key |
| In-process mutex per (org, provider) | `TryLock` for the refresh (≤ 30 s) | Already held → report `in_progress`, do not start a second refresh (across instances a duplicate refresh is harmless: idempotent upserts) |

## Business Rules

### Canonicalisation — `catalog.Canonicalize(provider, id)`

Deterministic. The same function applies to provider listings and to caller-supplied names, so a caller who sends a provider-specific id still resolves.

1. `s = lower(trim(id))`.
2. Override table (`internal/catalog/overrides.go`, `map[[2]string]string` keyed by `{provider or "", s}`) → return the mapped value if present.
3. Strip a leading `models/` (Gemini listing format).
4. Split off an OpenRouter-style variant `:<tag>` (e.g. `:free`, `:beta`, `:nitro`, `:thinking`) and re-attach it at the end. Assumed: variants are distinct canonical models (`llama-3.3-70b:free` ≠ `llama-3.3-70b`) because they have different limits and prices.
5. Strip a vendor prefix: keep the text after the last `/` (`meta-llama/llama-3.3-70b-instruct` → `llama-3.3-70b-instruct`).
6. `meta-llama-` prefix → `llama-` (Together/NVIDIA style).
7. Repeatedly strip trailing tokens: date stamps (`-YYYYMMDD`, `-YYYY-MM-DD`), pure-digit segments of 3–5 digits (`-001`, `-0125`, `-32768`, `-8192`), and the suffixes `-latest`, `-versatile`, `-instruct`, `-instant`, `-chat`, `-it`, `-hf`.
8. Family rule: for `claude-*`, replace `.` with `-` (`claude-sonnet-4.5` → `claude-sonnet-4-5`; `claude-sonnet-5-5-20261001` → `claude-sonnet-5-5`). Other families keep dots (`llama-3.3-70b`).
9. Collapse repeated `-`; trim `-`.

Examples (fixture table for unit tests):

| Provider | Provider model id | Canonical |
|----------|-------------------|-----------|
| groq | `llama-3.3-70b-versatile` | `llama-3.3-70b` |
| openrouter | `meta-llama/llama-3.3-70b-instruct` | `llama-3.3-70b` |
| openrouter | `meta-llama/llama-3.3-70b-instruct:free` | `llama-3.3-70b:free` |
| groq | `llama-3.1-8b-instant` | `llama-3.1-8b` |
| openrouter | `meta-llama/llama-3.1-8b-instruct` | `llama-3.1-8b` |
| groq | `mixtral-8x7b-32768` | `mixtral-8x7b` |
| groq | `gemma2-9b-it` | `gemma2-9b` |
| anthropic | `claude-sonnet-5-5-20261001` | `claude-sonnet-5-5` |
| openrouter | `anthropic/claude-sonnet-5.5` | `claude-sonnet-5-5` |
| google | `models/gemini-2.5-flash` | `gemini-2.5-flash` |
| google | `gemini-1.5-flash-001` | `gemini-1.5-flash` |
| (caller) | `Llama-3.3-70B-Versatile` | `llama-3.3-70b` |

Assumed: the exact Anthropic and Mesh API ids above are illustrative; the rules, not the specific ids, are what the tests check.

### Discovery

- Triggers (Phase 1): key added (async); `POST /api/keys/{id}/test` ok; `POST /api/catalog/refresh`; periodic job every `ROUTER0_CATALOG_REFRESH_HOURS` (6) for every (org, provider) with an active key; server startup for (org, provider) pairs never refreshed.
- Key choice: the provider's active keys in `position` order. The first key whose listing succeeds is used.
- Kind: ids containing `embed` → `embedding`; Groq `whisper*`, `distil-whisper*`, `*tts*`, `*guard*` are skipped (extends `isNonChatModel`); everything else is `chat`.
- OpenRouter: the current filter that keeps only `:free` models is removed; all listed models are stored. Assumed: paid models are wanted; whether a key can afford them shows up at request time as 402.
- OpenRouter `ListModels` must use `OPENROUTER_BASE_URL` (currently hard-coded), so tests can mock it.
- Upsert all listed rows (`is_available=true`, `last_seen_at=NOW()`, pricing/context from the listing or `groqPricing`), then mark unlisted rows of that org+provider `is_available=false`. Write `org_catalog_refreshes`.
- An attempt that ends `model_unavailable` sets just that row `is_available=false`.

### Resolution — `Resolver.Resolve(ctx, orgID, requested)`

1. (P2) `a = lower(trim(requested))`; if `org_model_aliases(org, a)` exists → `canonical = target` (one level, no chains).
2. Otherwise `canonical = Canonicalize("", requested)`.
3. Rows = `org_model_catalog WHERE org_id AND canonical_model = canonical AND is_available AND kind = <endpoint kind>`, restricted to providers with ≥1 active key.
4. Per provider, pick exactly one `provider_model_id`: (a) the one equal to `lower(trim(requested))` if present, else (b) the shortest, then (c) lexicographically smallest.
5. (P2) No rows → on-demand discovery. Assumed: the brief's "retroactively add based on request" means re-listing the org's providers at request time and adding the model if a provider now offers it. It does not mean guessing or creating rows for models no provider lists. Steps: if the in-memory miss-debounce entry for (org, canonical) is absent, set it (60 s TTL) and refresh every provider of the org whose last refresh is older than 60 s (parallel, 10 s total cap), then repeat steps 3–4. Rows inserted by this path get `source='on_demand'`. If the entry already exists, skip refreshing (debounce) and return not found.
6. Still no rows → `nil, nil` → routing returns `404 model_not_found`.

### `/v1/models`

Lists distinct available canonical models (plus aliases in P2) with providers, as in api.md. Ordered alphabetically.

## Success Criteria

- [ ] Unit: every row of the fixture table above passes `Canonicalize`.
- [ ] Integration: with mock Groq listing `llama-3.3-70b-versatile` and mock OpenRouter listing `meta-llama/llama-3.3-70b-instruct`, adding one key for each makes `GET /v1/models` (service token) contain `llama-3.3-70b` with both providers, within 3 s of the key add.
- [ ] A chat request with `model: "meta-llama/llama-3.3-70b-instruct"` resolves to canonical `llama-3.3-70b` and, if Groq is first, is served by Groq `llama-3.3-70b-versatile` (header `X-Router0-Model`).
- [ ] When the mock Groq listing drops a model and `POST /api/catalog/refresh` runs, the row becomes `is_available=false` and the model no longer resolves to Groq.
- [ ] A listing call with a key that gets 401 marks that key `error`, and discovery succeeds with the provider's next key.
- [ ] (P2) A mock provider that starts listing `new-model-x` after the org's last refresh: the first request for `new-model-x` succeeds via on-demand discovery, and the row has `source='on_demand'`.
- [ ] (P2) A second unknown-model request within 60 s does not call any provider `/models` again (mock call counter).
- [ ] (P2) Alias `fast → llama-3.3-70b`: a request with `model: "fast"` is served, and the response `model` is `llama-3.3-70b`.
